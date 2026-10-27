package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// verify checks a sink, record by record, against the op sequence the loader
// wrote. It replays the sequence from the same generator flags and -total, so
// it knows every change that was committed: its table, id, kind and payload.
//
// Event sinks (file, kafka, s3, http: one record per change) report, per kind:
//   - missing: a committed change the sink never delivered (loss);
//   - duplicates: deliveries beyond the first of the same change;
//   - malformed: a record whose marker names a change but whose id or payload
//     length does not match it (corruption);
//   - reordered: per source table, deliveries whose change is older than one
//     already delivered from the same table (first deliveries only, so a
//     redelivery after a restart is a duplicate, not a reorder). Only
//     meaningful where the sink preserves order: one file or one single-
//     partition topic per table. S3 object order and multi-partition topics
//     are reported as not checked.
//
// Inserts and updates are identified by the payload marker. Deletes carry no
// payload under REPLICA IDENTITY DEFAULT, so a delete is identified by its
// table and id (the loader deletes each id once, oldest first).
//
// Postgres sinks (upsert on the key) cannot show duplicates or order; verify
// compares the final table state with the replayed final state: rows missing,
// rows that should have been deleted, and rows whose payload is not the last
// write of that id. A lost INTERMEDIATE update (a later write of the same id
// arrived) is invisible to a final-state check; that limit is stated in
// PLAN.md.
//
// Kafka records are read as key, tab, value: a delete event or tombstone is
// attributed by the key's id and the topic's table.
func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	var (
		sink     = fs.String("sink", "", "one sink spec (see sinks.go)")
		total    = fs.Int64("total", 0, "workload ops the loader committed after its fill (its TOTAL_DONE)")
		payload  = fs.Int("payload", 64, "payload size the loader used")
		ordered  = fs.Bool("ordered", true, "the sink preserves per-table order (one file or single-partition topic per table)")
		noDelete = fs.Bool("no-deletes", false, "the pipeline drops delete events by design (unwrap to tombstone); deletes are not expected")
		out      = fs.String("out", "", "write the JSON report here as well as stdout")
		pgURL    = fs.String("pg", "postgres://bench:bench@cdcparity-postgres:5432/bench?sslmode=disable", "Postgres URL")
		brokers  = fs.String("brokers", "cdcparity-kafka:9092", "Kafka bootstrap servers")
		s3URL    = fs.String("s3", "http://cdcparity-minio:9000", "S3 endpoint")
	)
	gc := genFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := gc.validate(); err != nil {
		return err
	}
	specs, err := parseSinks(*sink)
	if err != nil || len(specs) != 1 {
		return fmt.Errorf("-sink must be exactly one sink spec")
	}
	spec := specs[0]
	e := env{pgURL: *pgURL, brokers: *brokers, s3URL: *s3URL, s3Key: "conduitminio", s3Sec: "conduitminiosecret", tables: gc.Tables}
	ctx := context.Background()

	ex := replay(*gc, *total)
	var rep map[string]any
	start := time.Now()
	if spec.Kind == "pg" {
		rep, err = verifyPG(ctx, e, spec.Arg, ex, *payload)
	} else {
		v := newEventVerifier(ex, *payload, *ordered && spec.Kind != "s3", *noDelete)
		switch spec.Kind {
		case "file":
			err = eachFileLine(spec.Arg, v.line)
		case "http":
			err = eachFileLine(spec.Arg, v.line) // the receiver's dump file
		case "kafka":
			err = eachKafkaRecord(ctx, e, spec.Arg, v.line)
		case "s3":
			err = eachS3Line(ctx, e, spec.Arg, v.line)
		}
		rep = v.report()
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", spec, err)
	}
	rep["sink"] = spec.String()
	rep["total_ops"] = *total
	rep["fill_rows"] = int64(gc.Tables) * gc.Window
	rep["verify_seconds"] = time.Since(start).Seconds()
	b, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(b))
	if *out != "" {
		return os.WriteFile(*out, append(b, '\n'), 0o644)
	}
	return nil
}

// expected is the replayed op sequence, indexed for lookups.
type expected struct {
	tables int
	ops    [][]op            // per table, in sequence order
	bySeq  []map[int64]int   // per table: seq -> index in ops (inserts, updates)
	delAt  []map[int64]int   // per table: deleted id -> index in ops
	final  []map[int64]int64 // per table: live id -> seq of its last write
}

func replay(gc genConfig, total int64) *expected {
	ex := &expected{tables: gc.Tables}
	ex.ops = make([][]op, gc.Tables)
	ex.bySeq = make([]map[int64]int, gc.Tables)
	ex.delAt = make([]map[int64]int, gc.Tables)
	ex.final = make([]map[int64]int64, gc.Tables)
	for t := 0; t < gc.Tables; t++ {
		ex.bySeq[t], ex.delAt[t], ex.final[t] = map[int64]int{}, map[int64]int{}, map[int64]int64{}
	}
	g := newGenerator(gc)
	add := func(o op) {
		i := len(ex.ops[o.Table])
		ex.ops[o.Table] = append(ex.ops[o.Table], o)
		if o.Kind == opDelete {
			ex.delAt[o.Table][o.ID] = i
			delete(ex.final[o.Table], o.ID)
		} else {
			ex.bySeq[o.Table][o.Seq] = i
			ex.final[o.Table][o.ID] = o.Seq
		}
	}
	for g.filling() {
		add(g.next())
	}
	for i := int64(0); i < total; i++ {
		add(g.next())
	}
	return ex
}

var (
	reTable = regexp.MustCompile(`bench_source_([0-9]+)`)
	reID    = regexp.MustCompile(`"id"\s*:\s*([0-9]+)|\bid=([0-9]+)`)
	reFile  = regexp.MustCompile(`_([0-9]+)\.[a-z]+$`)
)

type eventVerifier struct {
	ex        *expected
	payload   int
	ordered   bool
	noDelete  bool
	seen      [][]uint16 // per table, per op index: deliveries
	maxIdx    []int
	reordered [3]int64
	malformed int64
	unknown   int64
	unattrib  int64
	lines     int64
	samples   map[string][]string
}

func newEventVerifier(ex *expected, payload int, ordered, noDelete bool) *eventVerifier {
	v := &eventVerifier{ex: ex, payload: payload, ordered: ordered, noDelete: noDelete, samples: map[string][]string{}}
	v.seen = make([][]uint16, ex.tables)
	v.maxIdx = make([]int, ex.tables)
	for t := range v.seen {
		v.seen[t] = make([]uint16, len(ex.ops[t]))
		v.maxIdx[t] = -1
	}
	return v
}

func (v *eventVerifier) sample(class, s string) {
	if len(v.samples[class]) < 5 {
		if len(s) > 300 {
			s = s[:300] + "..."
		}
		v.samples[class] = append(v.samples[class], s)
	}
}

func (v *eventVerifier) line(source, line string) {
	v.lines++
	if line == "" || line == "null" || line == "\t" || line == "\tnull" {
		v.unattrib++ // a tombstone or empty record carries no change
		v.sample("unattributed", source+": "+line)
		return
	}
	if ms := findMarkers(line); len(ms) > 0 {
		m := ms[len(ms)-1] // the after image when a before image is present
		if m.Table < 0 || m.Table >= v.ex.tables {
			v.unknown++
			v.sample("unknown", line)
			return
		}
		i, ok := v.ex.bySeq[m.Table][m.Seq]
		if !ok {
			v.unknown++
			v.sample("unknown", line)
			return
		}
		o := v.ex.ops[m.Table][i]
		if m.Len != v.payload || !hasID(line, o.ID) {
			v.malformed++
			v.sample("malformed", line)
		}
		v.deliver(m.Table, i, line)
		return
	}
	// No marker: a delete, identified by table and id. The table comes from
	// the record (a Debezium envelope names it), else from where the record
	// was read: the topic (cdcparity.public.bench_source_N/partition, which is
	// all a flattened delete's tombstone has), else the file name.
	t := -1
	if mm := reTable.FindStringSubmatch(line); mm != nil {
		t, _ = strconv.Atoi(mm[1])
	} else if mm := reTable.FindStringSubmatch(source); mm != nil {
		t, _ = strconv.Atoi(mm[1])
	} else if mm := reFile.FindStringSubmatch(source); mm != nil {
		t, _ = strconv.Atoi(mm[1])
	} else if v.ex.tables == 1 {
		t = 0
	}
	idm := reID.FindStringSubmatch(line)
	if t < 0 || t >= v.ex.tables || idm == nil {
		v.unattrib++
		v.sample("unattributed", source+": "+line)
		return
	}
	id, _ := strconv.ParseInt(idm[1]+idm[2], 10, 64)
	i, ok := v.ex.delAt[t][id]
	if !ok {
		v.unknown++
		v.sample("unknown", line)
		return
	}
	v.deliver(t, i, line)
}

func hasID(line string, id int64) bool {
	for _, m := range reID.FindAllStringSubmatch(line, -1) {
		if m[1]+m[2] == strconv.FormatInt(id, 10) {
			return true
		}
	}
	return false
}

func (v *eventVerifier) deliver(t, i int, line string) {
	if v.seen[t][i] < 1<<16-1 {
		v.seen[t][i]++
	}
	if v.seen[t][i] > 1 {
		return
	}
	if v.ordered {
		if i < v.maxIdx[t] {
			v.reordered[v.ex.ops[t][i].Kind]++
			v.sample("reordered", line)
		} else {
			v.maxIdx[t] = i
		}
	}
}

func (v *eventVerifier) report() map[string]any {
	var exp, miss, dup [3]int64
	for t := range v.seen {
		for i, n := range v.seen[t] {
			k := v.ex.ops[t][i].Kind
			if k == opDelete && v.noDelete {
				continue
			}
			exp[k]++
			if n == 0 {
				miss[k]++
				if len(v.samples["missing"]) < 5 {
					o := v.ex.ops[t][i]
					v.samples["missing"] = append(v.samples["missing"], fmt.Sprintf("table %d seq %d id %d %s", o.Table, o.Seq, o.ID, o.Kind))
				}
			} else if n > 1 {
				dup[k] += int64(n - 1)
			}
		}
	}
	byKind := func(a [3]int64) map[string]int64 {
		return map[string]int64{"insert": a[opInsert], "update": a[opUpdate], "delete": a[opDelete], "all": a[0] + a[1] + a[2]}
	}
	rep := map[string]any{
		"kind":         "events",
		"lines":        v.lines,
		"expected":     byKind(exp),
		"missing":      byKind(miss),
		"duplicates":   byKind(dup),
		"malformed":    v.malformed,
		"unknown":      v.unknown,
		"unattributed": v.unattrib,
		"samples":      v.samples,
	}
	if v.ordered {
		rep["reordered"] = byKind(v.reordered)
	} else {
		rep["reordered"] = "not checked (sink does not preserve a per-table order)"
	}
	// A record nobody can attribute (a blank, a tombstone with no key) is a
	// failure too: a sink that writes blanks where data should be must not pass.
	rep["ok"] = miss[0]+miss[1]+miss[2] == 0 && v.malformed == 0 && v.unknown == 0 && v.unattrib == 0 &&
		(!v.ordered || v.reordered[0]+v.reordered[1]+v.reordered[2] == 0)
	rep["summary"] = fmt.Sprintf("missing %d, duplicates %d (%.1f per million), malformed %d, unknown %d, unattributed %d, reordered %v",
		miss[0]+miss[1]+miss[2], dup[0]+dup[1]+dup[2], perMillion(dup[0]+dup[1]+dup[2], exp[0]+exp[1]+exp[2]),
		v.malformed, v.unknown, v.unattrib, rep["reordered"])
	return rep
}

func verifyPG(ctx context.Context, e env, pattern string, ex *expected, payload int) (map[string]any, error) {
	conn, err := pgx.Connect(ctx, e.pgURL)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var expRows, missing, extra, stale, malformed int64
	samples := map[string][]string{}
	sample := func(c, s string) {
		if len(samples[c]) < 5 {
			samples[c] = append(samples[c], s)
		}
	}
	for t, tbl := range pgTables(pattern, ex.tables) {
		got := map[int64]bool{}
		rows, err := conn.Query(ctx, "SELECT id, payload FROM "+pgx.Identifier{tbl}.Sanitize())
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var p string
			if err := rows.Scan(&id, &p); err != nil {
				rows.Close()
				return nil, err
			}
			got[id] = true
			want, live := ex.final[t][id]
			if !live {
				extra++
				sample("extra", fmt.Sprintf("%s id %d", tbl, id))
				continue
			}
			ms := findMarkers(p)
			if len(ms) == 0 || len(p) != payload || ms[0].Table != t {
				malformed++
				sample("malformed", fmt.Sprintf("%s id %d payload %.80q", tbl, id, p))
				continue
			}
			if ms[0].Seq != want {
				stale++
				sample("stale", fmt.Sprintf("%s id %d has seq %d, last write was seq %d", tbl, id, ms[0].Seq, want))
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for id := range ex.final[t] {
			expRows++
			if !got[id] {
				missing++
				sample("missing", fmt.Sprintf("%s id %d", tbl, id))
			}
		}
	}
	return map[string]any{
		"kind":          "state",
		"expected_rows": expRows,
		"missing":       missing,
		"extra":         extra,
		"stale":         stale,
		"malformed":     malformed,
		"duplicates":    "not observable (upsert on the key)",
		"reordered":     "not observable directly; a reorder that changes the outcome shows as stale",
		"samples":       samples,
		"ok":            missing == 0 && extra == 0 && stale == 0 && malformed == 0,
	}, nil
}

func perMillion(n, of int64) float64 {
	if of == 0 {
		return 0
	}
	return float64(n) / float64(of) * 1e6
}
