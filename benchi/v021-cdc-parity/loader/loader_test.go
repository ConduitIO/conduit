package main

import (
	"bytes"
	"compress/flate"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

var testRnd = rand.New(rand.NewSource(7))

func testGen() genConfig {
	return genConfig{Tables: 3, Window: 50, Batch: 7, InsertFrac: 0.7, UpdateFrac: 0.2, Seed: 42}
}

// The verifier's whole premise: the sequence depends on the op count alone,
// not on how many ops each batch pulled (the -total tail, -conns grouping).
func TestGeneratorIsAFunctionOfOpCount(t *testing.T) {
	a, b := newGenerator(testGen()), newGenerator(testGen())
	for a.filling() {
		a.next()
		b.next()
	}
	for i := 0; i < 5000; i++ {
		if x, y := a.next(), b.next(); x != y {
			t.Fatalf("op %d differs: %+v vs %+v", i, x, y)
		}
	}
}

func TestGeneratorInvariants(t *testing.T) {
	g := newGenerator(testGen())
	live := make([]map[int64]bool, 3)
	seq := make([]int64, 3)
	for i := range live {
		live[i] = map[int64]bool{}
	}
	for i := 0; i < 20000; i++ {
		o := g.next()
		if o.Seq != seq[o.Table] {
			t.Fatalf("table %d: seq %d, want %d", o.Table, o.Seq, seq[o.Table])
		}
		seq[o.Table]++
		switch o.Kind {
		case opInsert:
			if live[o.Table][o.ID] {
				t.Fatalf("insert of live id %d", o.ID)
			}
			live[o.Table][o.ID] = true
		case opUpdate, opDelete:
			if !live[o.Table][o.ID] {
				t.Fatalf("%s of dead id %d", o.Kind, o.ID)
			}
			if o.Kind == opDelete {
				delete(live[o.Table], o.ID)
			}
		}
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	now := time.Unix(1793000000, 123456789)
	for _, size := range []int{48, 64, 1024, 16384} {
		o := op{Kind: opUpdate, Table: 12, ID: 99, Seq: 1234567}
		p := encodePayload(o, size, now, rand.New(rand.NewSource(int64(size))))
		if len(p) != size {
			t.Fatalf("size %d: payload is %d bytes", size, len(p))
		}
		// Embedded the way each sink writes it.
		for _, line := range []string{
			p,
			`{"before":null,"after":{"id":99,"payload":"` + p + `"},"op":"u"}`,
			`{"schema":{},"payload":{"id":99,"payload":"` + p + `"}}`,
			`{"key":{"id":99},"payload":{"after":{"payload":"` + p + `"}}}`,
			"{before=null, after=Struct{id=99,payload=" + p + "}}",
			"\x00\x00\x00\x00\x07\xc6\x01\x80\x01" + p + "\x02\x0eS9ab~x", // Avro bytes around the string
		} {
			ms := findMarkers(line)
			if len(ms) != 1 {
				t.Fatalf("size %d: %d markers in %.60q", size, len(ms), line)
			}
			if m := ms[0]; m.Table != 12 || m.Seq != 1234567 || m.TS != now.UnixNano() || m.Len != size {
				t.Fatalf("size %d: decoded %+v", size, m)
			}
		}
	}
	if ms := findMarkers(`{"id":5,"msg":"S1.2|T1234567890123456789|abc"}`); len(ms) != 0 { // no '~' sentinel
		t.Fatalf("unterminated marker accepted: %+v", ms)
	}
	if ms := findMarkers(`{"id":5,"msg":"xS1.2|T123|"}`); len(ms) != 0 {
		t.Fatalf("false marker: %+v", ms)
	}
}

// sinkLines renders the replayed sequence the way a Debezium file sink writes
// it: one line per change, deletes as a before image with only the key.
func sinkLines(ex *expected, payload int) [][]string {
	out := make([][]string, ex.tables)
	for tb, ops := range ex.ops {
		for _, o := range ops {
			var l string
			if o.Kind == opDelete {
				l = fmt.Sprintf(`{"before":{"id":%d},"after":null,"source":{"table":"bench_source_%d"},"op":"d"}`, o.ID, tb)
			} else {
				l = fmt.Sprintf(`{"before":null,"after":{"id":%d,"payload":"%s"},"op":"c"}`, o.ID, encodePayload(o, payload, time.Now(), testRnd))
			}
			out[tb] = append(out[tb], l)
		}
	}
	return out
}

func feed(v *eventVerifier, lines [][]string) {
	for tb, ls := range lines {
		for _, l := range ls {
			v.line(fmt.Sprintf("/sink/out_%d.txt", tb), l)
		}
	}
}

func TestVerifyCleanDelivery(t *testing.T) {
	ex := replay(testGen(), 3000)
	v := newEventVerifier(ex, 64, true, false)
	feed(v, sinkLines(ex, 64))
	rep := v.report()
	if rep["ok"] != true {
		t.Fatalf("clean delivery not ok: %+v", rep)
	}
}

func TestVerifyFindsLossDuplicatesReorderCorruption(t *testing.T) {
	ex := replay(testGen(), 3000)
	lines := sinkLines(ex, 64)
	// table 0: drop line 10 (loss), repeat line 20 at the end (redelivery,
	// a duplicate and not a reorder), swap 30/31 (reorder)
	l0 := lines[0]
	dropped := l0[10]
	l0 = append(append([]string{}, l0[:10]...), l0[11:]...)
	l0 = append(l0, l0[19])
	l0[29], l0[30] = l0[30], l0[29]
	lines[0] = l0
	// table 1: corrupt the payload length of one insert
	for i, l := range lines[1] {
		if strings.Contains(l, `"op":"c"`) {
			lines[1][i] = strings.Replace(l, "~", "A~", 1) // one byte too long
			break
		}
	}
	v := newEventVerifier(ex, 64, true, false)
	feed(v, lines)
	rep := v.report()
	if rep["ok"] != false {
		t.Fatal("damaged delivery reported ok")
	}
	if got := rep["missing"].(map[string]int64)["all"]; got != 1 {
		t.Fatalf("missing = %d, want 1 (%s)", got, dropped)
	}
	if got := rep["duplicates"].(map[string]int64)["all"]; got != 1 {
		t.Fatalf("duplicates = %d, want 1", got)
	}
	if got := rep["reordered"].(map[string]int64)["all"]; got != 1 {
		t.Fatalf("reordered = %d, want 1", got)
	}
	if got := rep["malformed"].(int64); got != 1 {
		t.Fatalf("malformed = %d, want 1", got)
	}
}

func TestScheduleParse(t *testing.T) {
	s, err := parseSchedule("20000:210s, 40000:3m30s,0:5m")
	if err != nil || len(s) != 3 || s[1].rate != 40000 || s[1].dur != 210*time.Second || s[2].rate != 0 {
		t.Fatalf("got %+v, %v", s, err)
	}
	if _, err := parseSchedule("20000"); err == nil {
		t.Fatal("missing duration accepted")
	}
}

// The padding must neither be free to compress (a run of one byte) nor
// incompressible: about 2x, so compression on or off is a real trade-off.
func TestPayloadCompressibility(t *testing.T) {
	var raw bytes.Buffer
	g := newGenerator(testGen())
	for i := 0; i < 2000; i++ {
		raw.WriteString(encodePayload(g.next(), 1024, time.Now(), testRnd))
	}
	var z bytes.Buffer
	w, _ := flate.NewWriter(&z, flate.BestSpeed)
	w.Write(raw.Bytes())
	w.Close()
	ratio := float64(raw.Len()) / float64(z.Len())
	if ratio < 1.6 || ratio > 3.0 {
		t.Fatalf("compression ratio %.2f, want about 2 (1.6-3.0)", ratio)
	}
	t.Logf("deflate(BestSpeed) ratio %.2f", ratio)
}

func TestZipfSkewIsDeterministic(t *testing.T) {
	cfg := testGen()
	cfg.Tables, cfg.Skew = 16, 1.2
	a, b := newGenerator(cfg), newGenerator(cfg)
	hot := 0
	for a.filling() {
		a.next()
		b.next()
	}
	for i := 0; i < 20000; i++ {
		x, y := a.next(), b.next()
		if x != y {
			t.Fatalf("op %d differs", i)
		}
		if x.Table == 0 {
			hot++
		}
	}
	if hot < 20000/16*2 {
		t.Fatalf("table 0 got %d of 20000 ops: not skewed", hot)
	}
}

func TestVerifyKafkaKeyAttributesTombstonesAndBlanksFail(t *testing.T) {
	ex := replay(genConfig{Tables: 1, Window: 3, Batch: 1, InsertFrac: 0.5, UpdateFrac: 0, Seed: 3}, 6)
	v := newEventVerifier(ex, 64, true, false)
	for _, o := range ex.ops[0] {
		key := fmt.Sprintf(`{"id":%d}`, o.ID)
		if o.Kind == opDelete {
			v.line("cdcparity.public.bench_source_0/0", key+"\t") // tombstone: key only
		} else {
			v.line("cdcparity.public.bench_source_0/0", key+"\t"+`{"id":`+fmt.Sprint(o.ID)+`,"payload":"`+encodePayload(o, 64, time.Now(), testRnd)+`"}`)
		}
	}
	if rep := v.report(); rep["ok"] != true {
		t.Fatalf("key-attributed tombstones not accepted: %+v", rep)
	}
	v.line("x", "") // a blank where a record should be
	if rep := v.report(); rep["ok"] != false {
		t.Fatal("a blank record passed")
	}
}
