package main

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// writer turns generated ops into SQL. The op sequence (gen.go) is the same
// for every shape and transaction size; only how it is written changes.
//
// Transactions: pgx runs a whole Batch in ONE implicit transaction unless the
// batch carries its own BEGIN/COMMIT. With -txn N the writer queues BEGIN and
// COMMIT around every N statements, so one round trip carries several
// transactions; -txn 1 is one statement per transaction. -txn 0 keeps the
// v0.21 behaviour: one transaction per round trip of -batch statements.
type writer struct {
	shape   string
	payload int
	txn     int
	mu      sync.Mutex
	rnd     *rand.Rand // padding only; never touches the op sequence
}

func newWriter(shape string, payload, txn int) *writer {
	return &writer{shape: shape, payload: payload, txn: txn, rnd: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// wideCols are the wide shape's 28 typed columns after (id, payload). Their
// values derive from the op, so a re-run writes the same rows.
var wideCols = func() []string {
	types := []string{"int", "bigint", "numeric", "text", "varchar", "timestamptz", "date", "bool", "jsonb", "uuid", "float8", "int"}
	var out []string
	for i := 0; i < 28; i++ {
		out = append(out, fmt.Sprintf("c%02d_%s", i, types[i%len(types)]))
	}
	return out
}()

// WideDDL is the column list init.sql uses for the wide shape.
func WideDDL() string {
	sqlType := map[string]string{"int": "integer", "bigint": "bigint", "numeric": "numeric(14,2)", "text": "text",
		"varchar": "varchar(64)", "timestamptz": "timestamptz", "date": "date", "bool": "boolean", "jsonb": "jsonb",
		"uuid": "uuid", "float8": "double precision"}
	var parts []string
	for _, c := range wideCols {
		parts = append(parts, c+" "+sqlType[c[strings.Index(c, "_")+1:]])
	}
	return strings.Join(parts, ", ")
}

func wideValue(col string, o op) any {
	n := o.ID*31 + o.Seq
	switch col[strings.Index(col, "_")+1:] {
	case "int":
		return int32(n % 1_000_000)
	case "bigint":
		return n
	case "numeric":
		return fmt.Sprintf("%d.%02d", n%100000, n%100)
	case "text":
		return "text-" + strconv.FormatInt(n, 36)
	case "varchar":
		return "v" + strconv.FormatInt(n, 16)
	case "timestamptz":
		return time.Unix(1_700_000_000+n%10_000_000, 0).UTC()
	case "date":
		return time.Unix(1_700_000_000+(n%3650)*86400, 0).UTC()
	case "bool":
		return n%2 == 0
	case "jsonb":
		return fmt.Sprintf(`{"k":%d,"tag":"t%d"}`, n%1000, n%7)
	case "uuid":
		return fmt.Sprintf("%08x-0000-4000-8000-%012x", uint32(o.ID), uint64(o.Seq))
	default: // float8
		return float64(n) / 7.0
	}
}

func (w *writer) pay(o op, now time.Time) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return encodePayload(o, w.payload, now, w.rnd)
}

func (w *writer) insertSQL(table string) string {
	if w.shape != "wide" {
		return "INSERT INTO " + table + " (id, payload) VALUES ($1, $2)"
	}
	var ph []string
	for i := range wideCols {
		ph = append(ph, "$"+strconv.Itoa(i+3))
	}
	return "INSERT INTO " + table + " (id, payload, " + strings.Join(wideCols, ", ") + ") VALUES ($1, $2, " + strings.Join(ph, ", ") + ")"
}

func (w *writer) insertArgs(o op, now time.Time) []any {
	args := []any{o.ID, w.pay(o, now)}
	if w.shape == "wide" {
		for _, c := range wideCols {
			args = append(args, wideValue(c, o))
		}
	}
	return args
}

// fill inserts every table's initial window with COPY, in sequence order.
func (w *writer) fill(ctx context.Context, conn *pgx.Conn, gen *generator, prog *progress) error {
	const chunk = 10000
	cols := []string{"id", "payload"}
	if w.shape == "wide" {
		cols = append(cols, wideCols...)
	}
	rows := make([][]any, 0, chunk)
	table := -1
	flush := func() error {
		if len(rows) == 0 {
			return nil
		}
		_, err := conn.CopyFrom(ctx, pgx.Identifier{fmt.Sprintf("bench_source_%d", table)}, cols, pgx.CopyFromRows(rows))
		if err == nil {
			prog.Inserts.Add(int64(len(rows)))
		}
		rows = rows[:0]
		return err
	}
	for gen.filling() {
		o := gen.next()
		if o.Table != table {
			if err := flush(); err != nil {
				return err
			}
			table = o.Table
		}
		rows = append(rows, w.insertArgs(o, time.Now()))
		if len(rows) == chunk {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// alter adds a nullable column to every source table (schema evolution mid-run).
func (w *writer) alter(ctx context.Context, conn *pgx.Conn, tables int) error {
	for i := 0; i < tables; i++ {
		if _, err := conn.Exec(ctx, fmt.Sprintf("ALTER TABLE bench_source_%d ADD COLUMN evolved text", i)); err != nil {
			return err
		}
	}
	return nil
}

// execParallel runs each connection's ops as one pgx.Batch, all connections
// concurrently, and returns once every batch has committed.
func (w *writer) execParallel(ctx context.Context, cs []*pgx.Conn, batches [][]op, prog *progress) error {
	if len(cs) == 1 {
		return w.exec(ctx, cs[0], batches[0], prog)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(cs))
	for i := range cs {
		if len(batches[i]) == 0 {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = w.exec(ctx, cs[i], batches[i], prog)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *writer) exec(ctx context.Context, conn *pgx.Conn, ops []op, prog *progress) error {
	if len(ops) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	now := time.Now()
	var ins, upd, del int64
	inTxn := 0
	for _, o := range ops {
		if w.txn > 0 && inTxn == 0 {
			b.Queue("BEGIN")
		}
		table := "bench_source_" + strconv.Itoa(o.Table)
		switch o.Kind {
		case opInsert:
			b.Queue(w.insertSQL(table), w.insertArgs(o, now)...)
			ins++
		case opUpdate:
			if w.shape == "wide" {
				b.Queue("UPDATE "+table+" SET payload = $2, c05_timestamptz = $3, c02_numeric = $4 WHERE id = $1",
					o.ID, w.pay(o, now), wideValue("c05_timestamptz", o), wideValue("c02_numeric", o))
			} else {
				b.Queue("UPDATE "+table+" SET payload = $2 WHERE id = $1", o.ID, w.pay(o, now))
			}
			upd++
		case opDelete:
			b.Queue("DELETE FROM "+table+" WHERE id = $1", o.ID)
			del++
		}
		if w.txn > 0 {
			inTxn++
			if inTxn == w.txn {
				b.Queue("COMMIT")
				inTxn = 0
			}
		}
	}
	if w.txn > 0 && inTxn > 0 {
		b.Queue("COMMIT")
	}
	br := conn.SendBatch(ctx, b)
	for i := 0; i < b.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return err
		}
	}
	if err := br.Close(); err != nil {
		return err
	}
	prog.Inserts.Add(ins)
	prog.Updates.Add(upd)
	prog.Deletes.Add(del)
	return nil
}
