package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

// progress is the loader's committed work, served on -http. Counts move only
// after a batch's transactions have all committed (see writer.exec), so they
// never run ahead of what Postgres has committed.
type progress struct {
	Inserts atomic.Int64
	Updates atomic.Int64
	Deletes atomic.Int64
	Step    atomic.Int64 // index of the current -schedule step
	Target  atomic.Int64 // current target rate, 0 = unlimited
	Done    atomic.Bool  // -total reached or -fill-only finished
}

func (p *progress) serve(addr string) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/progress", func(w http.ResponseWriter, _ *http.Request) {
		ins, upd, del := p.Inserts.Load(), p.Updates.Load(), p.Deletes.Load()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ts_ms":  time.Now().UnixMilli(),
			"events": ins + upd + del,
			"net":    ins - del,
			"step":   p.Step.Load(),
			"target": p.Target.Load(),
			"done":   p.Done.Load(),
		})
	})
	go func() { log.Printf("loader: progress server: %v", http.ListenAndServe(addr, mux)) }()
}

type step struct {
	rate int
	dur  time.Duration
}

// parseSchedule parses "20000:210s,40000:210s". The last step holds until the
// loader is stopped.
func parseSchedule(s string) ([]step, error) {
	var out []step
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		r, d, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("schedule step %q: want rate:duration", part)
		}
		rate, err := strconv.Atoi(r)
		if err != nil || rate < 0 {
			return nil, fmt.Errorf("schedule step %q: bad rate", part)
		}
		dur, err := time.ParseDuration(d)
		if err != nil || dur <= 0 {
			return nil, fmt.Errorf("schedule step %q: bad duration", part)
		}
		out = append(out, step{rate, dur})
	}
	return out, nil
}

func genFlags(fs *flag.FlagSet) *genConfig {
	g := &genConfig{}
	fs.IntVar(&g.Tables, "tables", 1, "number of source tables (bench_source_0 .. bench_source_N-1); the workload is split, not multiplied")
	fs.Int64Var(&g.Window, "window", 10000, "initial number of live rows per table (inserted first, part of the replayed sequence)")
	fs.IntVar(&g.Batch, "batch", 1000, "statements per round trip; also the round-robin period of the op sequence")
	fs.Float64Var(&g.InsertFrac, "insert-frac", 0.70, "fraction of INSERT operations")
	fs.Float64Var(&g.UpdateFrac, "update-frac", 0.20, "fraction of UPDATE operations")
	fs.Int64Var(&g.Seed, "seed", 0xC0FFEE, "op sequence seed")
	fs.Float64Var(&g.Skew, "skew", 0, "Zipf exponent (> 1) for choosing each op's table; 0 = round-robin")
	return g
}

func (g *genConfig) validate() error {
	if g.Tables < 1 {
		return fmt.Errorf("-tables must be >= 1")
	}
	if g.Batch < 1 {
		return fmt.Errorf("-batch must be >= 1")
	}
	if g.InsertFrac+g.UpdateFrac > 1.0 {
		return fmt.Errorf("insert-frac + update-frac must be <= 1.0")
	}
	return nil
}

func runLoad(args []string) error {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	var (
		url         = fs.String("url", "", "Postgres connection URL (required)")
		rate        = fs.Int("rate", 20000, "target operations per second (total, across all tables); 0 = unlimited")
		schedule    = fs.String("schedule", "", "offered-rate ramp, rate:duration[,rate:duration...]; overrides -rate")
		total       = fs.Int64("total", 0, "stop after this many workload ops (0 = run until stopped); the fill is not counted")
		payload     = fs.Int("payload", 64, "payload size in bytes (at least 48: the marker)")
		conns       = fs.Int("conns", 1, "Postgres connections; tables are assigned to connections by index")
		txn         = fs.Int("txn", 0, "statements per transaction (0 = one transaction per -batch round trip)")
		shape       = fs.String("shape", "narrow", "row shape: narrow (id, payload) or wide (id, payload + 28 typed columns)")
		alterAt     = fs.Duration("alter-at", 0, "if > 0: ALTER TABLE ... ADD COLUMN on every source table this long after the workload starts (schema evolution)")
		fillOnly    = fs.Bool("fill-only", false, "insert the initial window, then stop writing (snapshot cells)")
		doneFile    = fs.String("done-file", "", "with -fill-only: create this file once the fill is committed")
		httpAddr    = fs.String("http", ":9100", "address of the /progress endpoint (empty disables it)")
		slots       = fs.String("slots", "", "comma-separated pg_replication_slots to wait for before writing (empty = start immediately)")
		slotTimeout = fs.Duration("slot-timeout", 10*time.Minute, "how long to wait for the -slots to appear before failing")
	)
	gc := genFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *url == "" {
		return fmt.Errorf("-url is required")
	}
	if err := gc.validate(); err != nil {
		return err
	}
	if *payload < minPayload {
		return fmt.Errorf("-payload must be >= %d", minPayload)
	}
	if *conns < 1 {
		*conns = 1
	}
	steps := []step{{rate: *rate, dur: time.Duration(1<<63 - 1)}}
	if *schedule != "" {
		s, err := parseSchedule(*schedule)
		if err != nil {
			return err
		}
		if len(s) == 0 {
			return fmt.Errorf("-schedule is empty")
		}
		steps = s
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cs := make([]*pgx.Conn, *conns)
	for i := range cs {
		c, err := connect(ctx, *url)
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		defer c.Close(context.Background())
		cs[i] = c
	}

	var prog progress
	prog.serve(*httpAddr)

	log.Printf("loader: rate=%d schedule=%q total=%d tables=%d window=%d payload=%d batch=%d conns=%d insert=%.0f%% update=%.0f%% slots=%q",
		*rate, *schedule, *total, gc.Tables, gc.Window, *payload, gc.Batch, *conns, gc.InsertFrac*100, gc.UpdateFrac*100, *slots)

	if names := splitSlots(*slots); len(names) > 0 {
		log.Printf("loader: waiting up to %s for replication slot(s) %s", *slotTimeout, strings.Join(names, ", "))
		if err := waitForSlots(ctx, cs[0], names, *slotTimeout); err != nil {
			return fmt.Errorf("replication slot wait failed: %w", err)
		}
		log.Printf("loader: replication slot(s) present")
	}

	if *shape != "narrow" && *shape != "wide" {
		return fmt.Errorf("-shape must be narrow or wide")
	}
	w := newWriter(*shape, *payload, *txn)
	gen := newGenerator(*gc)
	fillStart := time.Now()
	if err := w.fill(ctx, cs[0], gen, &prog); err != nil {
		return fmt.Errorf("fill: %w", err)
	}
	log.Printf("loader: FILL_DONE tables=%d rows/table=%d in %s", gc.Tables, gc.Window, time.Since(fillStart).Round(time.Millisecond))
	if *fillOnly {
		prog.Done.Store(true)
		if err := touch(*doneFile); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}

	var (
		totalOps int64
		logEvery = time.Now().Add(5 * time.Second)
		logAtOps int64
		stepIdx  = 0
		stepEnd  = time.Now().Add(steps[0].dur)
	)
	setStep := func(i int) {
		prog.Step.Store(int64(i))
		prog.Target.Store(int64(steps[i].rate))
		log.Printf("loader: STEP %d rate=%d start_ms=%d", i, steps[i].rate, time.Now().UnixMilli())
	}
	setStep(0)

	batches := make([][]op, *conns)
	workStart := time.Now()
	altered := false
	for ctx.Err() == nil {
		if *alterAt > 0 && !altered && time.Since(workStart) >= *alterAt {
			if err := w.alter(ctx, cs[0], gc.Tables); err != nil {
				return fmt.Errorf("alter: %w", err)
			}
			altered = true
			log.Printf("loader: ALTER_MS=%d", time.Now().UnixMilli())
		}
		if *total > 0 && totalOps >= *total {
			prog.Done.Store(true)
			log.Printf("loader: TOTAL_DONE ops=%d", totalOps)
			<-ctx.Done()
			break
		}
		if now := time.Now(); now.After(stepEnd) && stepIdx < len(steps)-1 {
			stepIdx++
			stepEnd = now.Add(steps[stepIdx].dur)
			setStep(stepIdx)
		}

		n := gc.Batch
		if *total > 0 && *total-totalOps < int64(n) {
			n = int(*total - totalOps)
		}
		for i := range batches {
			batches[i] = batches[i][:0]
		}
		for i := 0; i < n; i++ {
			o := gen.next()
			batches[o.Table%*conns] = append(batches[o.Table%*conns], o)
		}

		start := time.Now()
		if err := w.execParallel(ctx, cs, batches, &prog); err != nil {
			if ctx.Err() != nil {
				break
			}
			// A failed statement is not retried: the replayed sequence would
			// no longer match what was committed, so `verify` would report
			// phantom losses. Fail loudly instead.
			return fmt.Errorf("batch: %w", err)
		}
		totalOps += int64(n)

		if r := steps[stepIdx].rate; r > 0 {
			want := time.Duration(n) * time.Second / time.Duration(r)
			if elapsed := time.Since(start); elapsed < want {
				time.Sleep(want - elapsed)
			}
		}

		if now := time.Now(); now.After(logEvery) {
			dt := now.Sub(logEvery.Add(-5 * time.Second)).Seconds()
			log.Printf("loader: %d ops/s (target %d), total %d", int64(float64(totalOps-logAtOps)/dt), steps[stepIdx].rate, totalOps)
			logAtOps = totalOps
			logEvery = now.Add(5 * time.Second)
		}
	}
	log.Printf("loader: stopping after %d total ops", totalOps)
	return nil
}

func connect(ctx context.Context, url string) (*pgx.Conn, error) {
	for attempt := 0; ; attempt++ {
		conn, err := pgx.Connect(ctx, url)
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt < 30 {
			time.Sleep(time.Second)
			continue
		}
		return nil, err
	}
}

// splitSlots parses the -slots flag into a de-duplicated list of slot names in
// first-seen order. Empty input yields nil, so the file-sink arms (which pass
// no -slots) skip the wait entirely and start writing immediately.
func splitSlots(s string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(s, ",") {
		name := strings.TrimSpace(part)
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// waitForSlots polls pg_replication_slots until every named slot exists, or
// until timeout or ctx cancellation.
func waitForSlots(ctx context.Context, conn *pgx.Conn, slots []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastMissing := slots
	for {
		present, err := replicationSlots(ctx, conn)
		if err != nil {
			log.Printf("loader: slot poll error (will retry): %v", err)
		} else {
			lastMissing = missingSlots(slots, present)
			if len(lastMissing) == 0 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for replication slot(s) %s: the snapshotMode=never CDC source never created them (is it stuck starting?)", timeout, strings.Join(lastMissing, ", "))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func replicationSlots(ctx context.Context, conn *pgx.Conn) (map[string]bool, error) {
	rows, err := conn.Query(ctx, "SELECT slot_name FROM pg_replication_slots")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	present := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		present[name] = true
	}
	return present, rows.Err()
}

func missingSlots(want []string, present map[string]bool) []string {
	var missing []string
	for _, s := range want {
		if !present[s] {
			missing = append(missing, s)
		}
	}
	return missing
}

// touch creates path (if non-empty), for hooks waiting on a fill.
func touch(path string) error {
	if path == "" {
		return nil
	}
	return os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o666)
}
