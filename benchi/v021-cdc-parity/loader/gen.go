package main

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// opKind is the kind of one source change.
type opKind uint8

const (
	opInsert opKind = iota
	opUpdate
	opDelete
)

func (k opKind) String() string {
	switch k {
	case opInsert:
		return "insert"
	case opUpdate:
		return "update"
	default:
		return "delete"
	}
}

// op is one source change. Seq is the per-table index of the op, counted over
// the table's initial fill and its workload ops together, so (Table, Seq)
// identifies every change the loader ever makes.
type op struct {
	Kind  opKind
	Table int
	ID    int64
	Seq   int64
}

// genConfig holds everything the op sequence depends on. The sequence depends
// on nothing else: not on time, rate or how the database answers. That is what
// lets `verify` replay exactly what `load` wrote from the same flags.
type genConfig struct {
	Tables     int
	Window     int64
	Batch      int
	InsertFrac float64
	UpdateFrac float64
	Seed       int64
	// Skew > 1 picks each op's table from a Zipf(Skew) distribution (table 0
	// hottest) instead of round-robin: many tables with realistic skew.
	Skew float64
}

// generator produces the loader's op sequence: first every table's initial
// fill (ids [0, Window), as inserts), then workload batches of Batch ops,
// round-robin across the tables within each batch (or Zipf-skewed, Skew).
type generator struct {
	cfg   genConfig
	rng   *rand.Rand
	zipf  *rand.Zipf
	low   []int64
	high  []int64
	seq   []int64
	slot  int // position inside the current workload batch
	fillT int // table currently being filled, == Tables once the fill is done
}

func newGenerator(cfg genConfig) *generator {
	g := &generator{
		cfg:  cfg,
		rng:  rand.New(rand.NewSource(cfg.Seed)),
		low:  make([]int64, cfg.Tables),
		high: make([]int64, cfg.Tables),
		seq:  make([]int64, cfg.Tables),
	}
	if cfg.Window <= 0 {
		g.fillT = cfg.Tables
	}
	if cfg.Skew > 1 && cfg.Tables > 1 {
		g.zipf = rand.NewZipf(g.rng, cfg.Skew, 1, uint64(cfg.Tables-1))
	}
	return g
}

// filling reports whether the next op belongs to the initial fill.
func (g *generator) filling() bool { return g.fillT < g.cfg.Tables }

// next returns the next op in the sequence.
func (g *generator) next() op {
	if g.filling() {
		t := g.fillT
		o := op{Kind: opInsert, Table: t, ID: g.high[t], Seq: g.seq[t]}
		g.high[t]++
		g.seq[t]++
		if g.high[t] >= g.cfg.Window {
			g.fillT++
		}
		return o
	}
	t := g.slot % g.cfg.Tables
	if g.zipf != nil {
		t = int(g.zipf.Uint64())
	}
	g.slot++
	if g.slot == g.cfg.Batch {
		g.slot = 0
	}
	roll := g.rng.Float64()
	o := op{Table: t, Seq: g.seq[t]}
	g.seq[t]++
	switch {
	case roll < g.cfg.InsertFrac:
		o.Kind, o.ID = opInsert, g.high[t]
		g.high[t]++
	case roll < g.cfg.InsertFrac+g.cfg.UpdateFrac && g.high[t] > g.low[t]:
		o.Kind, o.ID = opUpdate, g.low[t]+g.rng.Int63n(g.high[t]-g.low[t])
	case g.high[t]-g.low[t] > 1: // always keep at least one live row
		o.Kind, o.ID = opDelete, g.low[t]
		g.low[t]++
	default:
		o.Kind, o.ID = opInsert, g.high[t]
		g.high[t]++
	}
	return o
}

// The payload of every inserted or updated row encodes the change it came
// from, so a sink's output can be checked record by record:
//
//	S<table>.<seq>|T<unix nanos, 19 digits>|<padding>~
//
// exactly the configured payload size, '~' last. T is taken when the
// statement is queued, before its transaction commits; latency is measured
// from it. Nothing in it is escaped by JSON, CSV, Avro strings or a Kafka
// Connect struct's toString, so it survives every sink format unchanged.
//
// The padding is NOT a constant run (which every codec compresses to almost
// nothing and which would make compression free): it is groups of 8 random
// alphanumerics, each written twice, about 2x compressible by lz4 or snappy
// (TestPayloadCompressibility pins the ratio). The padding is not part of the
// replayed sequence; verify checks only its length.
const minPayload = 48

const padAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func encodePayload(o op, size int, now time.Time, rnd *rand.Rand) string {
	var b strings.Builder
	b.Grow(size)
	b.WriteByte('S')
	b.WriteString(strconv.Itoa(o.Table))
	b.WriteByte('.')
	b.WriteString(strconv.FormatInt(o.Seq, 10))
	b.WriteString("|T")
	b.WriteString(fmt.Sprintf("%019d", now.UnixNano()))
	b.WriteByte('|')
	var grp [8]byte
	for b.Len() < size-1 {
		for i := range grp {
			grp[i] = padAlphabet[rnd.Intn(len(padAlphabet))]
		}
		for rep := 0; rep < 2; rep++ {
			for _, c := range grp {
				if b.Len() < size-1 {
					b.WriteByte(c)
				}
			}
		}
	}
	b.WriteByte('~')
	return b.String()
}

// marker is a decoded payload marker.
type marker struct {
	Table int
	Seq   int64
	TS    int64 // unix nanos
	Len   int   // length of the whole payload string, marker included
}

// findMarkers returns every payload marker in line, in order. A sink line can
// hold more than one (a Debezium update with a before image, a batched HTTP
// body), so callers pick what they need.
func findMarkers(line string) []marker {
	var out []marker
	for i := 0; i < len(line); i++ {
		if line[i] != 'S' || (i > 0 && isAlnum(line[i-1])) {
			continue
		}
		m, n, ok := parseMarker(line[i:])
		if ok {
			out = append(out, m)
			i += n - 1
		}
	}
	return out
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

// parseMarker parses a marker at the start of s and returns it and the number
// of bytes of s the payload spans (marker plus padding).
func parseMarker(s string) (marker, int, bool) {
	var m marker
	i := 1
	t, j, ok := digits(s, i)
	if !ok || j >= len(s) || s[j] != '.' {
		return m, 0, false
	}
	seq, k, ok := digits(s, j+1)
	if !ok || k+2 > len(s) || s[k] != '|' || s[k+1] != 'T' {
		return m, 0, false
	}
	ts, l, ok := digits(s, k+2)
	if !ok || l-(k+2) != 19 || l >= len(s) || s[l] != '|' {
		return m, 0, false
	}
	end := l + 1
	for end < len(s) && s[end] != '~' && isAlnum(s[end]) && s[end] != '_' {
		end++
	}
	if end >= len(s) || s[end] != '~' {
		return m, 0, false
	}
	end++
	m.Table, m.Seq, m.TS, m.Len = int(t), seq, ts, end
	return m, end, true
}

func digits(s string, i int) (int64, int, bool) {
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == i || j-i > 19 {
		return 0, i, false
	}
	v, err := strconv.ParseInt(s[i:j], 10, 64)
	return v, j, err == nil
}
