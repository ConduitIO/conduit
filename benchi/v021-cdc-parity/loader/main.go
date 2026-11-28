// Copyright © 2026 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command loader writes a steady, rate-limited mix of INSERT/UPDATE/DELETE
// against the Postgres source tables that all CDC systems under test capture.
// It is the SAME loader, at the SAME rate, for every arm — the workload is
// identical by construction; only the CDC pipeline differs.
//
// # Workload
//
// The loader keeps, per table and per writer, a sliding window of live rows
// [low, high) and cycles:
//   - INSERT (default 70%): a new row with the next id
//   - UPDATE (default 20%): rewrite the payload of a random live row
//   - DELETE (default 10%): remove the oldest live row (log-retention style)
//
// With -tables N (default 1), the SAME total rate is split round-robin across
// bench_source_0 .. bench_source_{N-1}, each with its own window of -window
// live rows. The workload is SPLIT, not multiplied: total operations per
// second stay at -rate regardless of N, so raising N varies only the engine's
// parallelism (N independent sources/destinations), never the offered load.
//
// Each table therefore GROWS at (insert-delete) rate — an append-heavy,
// OLTP-like workload — while a continuous stream of all three operation kinds
// flows through the WAL. Updates and deletes always target a live row. Row
// shape (id bigint PRIMARY KEY, payload text of -payload bytes) is fixed, so
// the WAL volume per operation is identical across arms.
//
// # Writers, and why the loader has more than one
//
// -writers W (default 1) runs W writer goroutines on W independent Postgres
// connections, sharing one aggregate token bucket, so -rate still means total
// operations per second across the whole loader. W=1 is byte-for-byte the old
// single-connection loader: same per-writer RNG seed (0xC0FFEE), same
// statements, same order, same initial table contents.
//
// Each writer owns a DISJOINT id range per table: writer w of W owns exactly
// the ids congruent to w modulo W (slot k of writer w is id w+k*W). The ranges
// are disjoint by construction, so UPDATE and DELETE targeting stays
// deterministic (a writer only ever updates or deletes a row it inserted),
// writers never contend on the same row, and the union of all writers' initial
// fills is exactly the id set [0, -window) for ANY W — the table the arms
// capture does not depend on how many writers produced it. See writerShare and
// (*writer).id, and loader_test.go, which asserts all of that.
//
// A single connection is a hard ceiling: one pipelined round trip at a time
// means the loader tops out at (batch size / round-trip latency), measured at
// about 39.1k ops/s on the published c6i.2xlarge runs. That ceiling is lower
// than the capacity of the file and Kafka arms it was used to measure, which
// makes a saturation number impossible to obtain — the harness saturates
// before the system under test does. W writers raise the ceiling roughly
// linearly until the database, not the loader, is the limiter.
//
// -unbounded ignores -rate and runs flat out. Combined with the no-CDC-arm
// `loader-ceiling` arm in bench.yml, that is how the loader's own ceiling is
// MEASURED rather than assumed. The ceiling is a property of (writers, batch,
// payload, tables, box): re-measure it whenever any of those change, in the
// same session, on the same box as the arms it qualifies. README.md,
// "The ceiling rule", states the admissibility rule that uses it: a result
// where any arm is within 5% of the measured loader ceiling or of the measured
// WAL ceiling is inadmissible, because the harness was the limiter.
//
// A counter is logged every 5s so the achieved rate can be checked against
// -rate in the run logs; when the loader cannot deliver the offered rate it
// logs UNDERRUN, which means the run is loader-limited and -writers must be
// raised.
//
// # Startup ordering
//
// -slots (default "") is a comma-separated list of replication slot names to
// wait for before writing. The Postgres-destination arms mirror the source's
// LIVE rows, so anything written before the snapshotMode=never CDC source
// creates its replication slot would never be captured, and updates to those
// phantom rows would upsert-INSERT rows the snapshot never created, inflating
// the destination's count by however long the tool took to start. Instead of a
// blind delay, the loader polls pg_replication_slots and starts writing only
// once every named slot exists, bounded by -slot-timeout (default 10m) with a
// clear failure message. The file-sink arms count events (not net rows) and
// have no slot, so they pass no -slots and start writing immediately.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/time/rate"
)

// reportEvery is how often the loader logs its achieved aggregate rate.
const reportEvery = 5 * time.Second

// baseSeed seeds writer 0's RNG. Writer w uses baseSeed+w, so a one-writer
// loader produces exactly the statement sequence the pre-writers loader did.
const baseSeed = 0xC0FFEE

// underrunFrac is the fraction of the offered rate below which a reporting
// window is called out as an UNDERRUN: the loader, not the system under test,
// set the rate. It matches the 5% admissibility margin in README.md.
const underrunFrac = 0.95

func main() {
	cfg, err := parseFlags()
	if err != nil {
		log.Fatalf("loader: %v", err)
	}
	// Every failure below is returned, not fatal in place, so the deferred
	// signal-stop and connection closes actually run.
	if err := run(cfg); err != nil {
		log.Fatalf("loader: %v", err)
	}
}

// config is the parsed, validated command line. The workload fields (mix,
// payload, window, batch) are the MEASURED workload: changing their meaning
// invalidates every committed result, so they are documented in the package
// comment and asserted in loader_test.go.
type config struct {
	url         string
	rate        int
	writers     int
	unbounded   bool
	tables      int
	window      int
	payload     int
	batch       int
	insertFrac  float64
	updateFrac  float64
	slots       string
	slotTimeout time.Duration
}

// offered describes the offered load for the startup log: a rate, or the
// ceiling-measurement mode in which -rate is ignored.
func (c *config) offered() string {
	if c.unbounded {
		return "unbounded (ceiling measurement: -rate ignored)"
	}
	return fmt.Sprintf("%d ops/s", c.rate)
}

func parseFlags() (*config, error) {
	var (
		url         = flag.String("url", "", "Postgres connection URL (required)")
		targetRate  = flag.Int("rate", 20000, "target operations per second (total, across all tables and writers)")
		writers     = flag.Int("writers", 1, "writer goroutines, each on its own connection and owning a disjoint id range per table (1 = the historical single-connection loader)")
		unbounded   = flag.Bool("unbounded", false, "ignore -rate and write flat out: this measures the loader's own ceiling, never an arm's throughput")
		tables      = flag.Int("tables", 1, "number of source tables (bench_source_0 .. bench_source_N-1); the workload is split, not multiplied")
		window      = flag.Int("window", 10000, "initial number of live rows per table (split across writers)")
		payload     = flag.Int("payload", 64, "payload size in bytes")
		batch       = flag.Int("batch", 1000, "statements per round trip, per writer")
		insertFrac  = flag.Float64("insert-frac", 0.70, "fraction of INSERT operations")
		updateFrac  = flag.Float64("update-frac", 0.20, "fraction of UPDATE operations")
		slots       = flag.String("slots", "", "comma-separated pg_replication_slots to wait for before writing (empty = start immediately)")
		slotTimeout = flag.Duration("slot-timeout", 10*time.Minute, "how long to wait for the -slots to appear before failing")
	)
	flag.Parse()

	cfg := &config{
		url: *url, rate: *targetRate, writers: *writers, unbounded: *unbounded,
		tables: *tables, window: *window, payload: *payload, batch: *batch,
		insertFrac: *insertFrac, updateFrac: *updateFrac,
		slots: *slots, slotTimeout: *slotTimeout,
	}
	return cfg, cfg.validate()
}

// validate rejects a configuration that cannot produce the measured workload,
// at startup rather than halfway through a run.
func (c *config) validate() error {
	switch {
	case c.url == "":
		return fmt.Errorf("-url is required")
	case c.tables < 1:
		return fmt.Errorf("-tables must be >= 1")
	case c.writers < 1:
		return fmt.Errorf("-writers must be >= 1")
	case c.batch < 1:
		return fmt.Errorf("-batch must be >= 1")
	case !c.unbounded && c.rate < 1:
		return fmt.Errorf("-rate must be >= 1 (or pass -unbounded)")
	case c.window < c.writers:
		// Every writer needs at least one live row of its own in every table,
		// or its first UPDATE/DELETE has nothing to target. -window is split
		// across writers, so W writers need at least W initial rows per table.
		return fmt.Errorf("-window (%d) must be >= -writers (%d): each writer owns a disjoint slice of the initial window and needs at least one live row in it", c.window, c.writers)
	case c.insertFrac+c.updateFrac > 1.0:
		return fmt.Errorf("insert-frac + update-frac must be <= 1.0")
	}
	return nil
}

func run(cfg *config) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("loader: offered=%s writers=%d tables=%d window=%d/table payload=%d bytes batch=%d/writer insert=%.0f%% update=%.0f%% delete=%.0f%% slots=%q slot-timeout=%s",
		cfg.offered(), cfg.writers, cfg.tables, cfg.window, cfg.payload, cfg.batch,
		cfg.insertFrac*100, cfg.updateFrac*100, (1-cfg.insertFrac-cfg.updateFrac)*100, cfg.slots, cfg.slotTimeout)

	// Control connection: the slot wait only. The writers get their own.
	control, err := connect(ctx, cfg.url)
	if err != nil {
		return fmt.Errorf("connect (control): %w", err)
	}
	defer control.Close(context.Background())

	// Wait for the CDC source to create its replication slot before writing.
	// The Postgres-destination arms mirror the source's LIVE rows, so a row
	// written before the slot exists would never be snapshotted: updates to it
	// would then upsert-INSERT a phantom row and deletes of it would be a no-op,
	// inflating the destination's row count by however long the tool took to
	// start. Polling pg_replication_slots (rather than sleeping a fixed delay)
	// makes every write a clean steady-state change without depending on how
	// long the tool's startup happens to take. File-sink arms count events (not
	// net rows) and pass no -slots, so they are unaffected and start immediately.
	if slotNames := splitSlots(cfg.slots); len(slotNames) > 0 {
		log.Printf("loader: waiting up to %s for replication slot(s) %s", cfg.slotTimeout, strings.Join(slotNames, ", "))
		if err := waitForSlots(ctx, control, slotNames, cfg.slotTimeout); err != nil {
			return fmt.Errorf("replication slot wait failed: %w", err)
		}
		log.Printf("loader: replication slot(s) present: %s", strings.Join(slotNames, ", "))
	}

	tableNames := make([]string, cfg.tables)
	for t := range tableNames {
		tableNames[t] = fmt.Sprintf("bench_source_%d", t)
	}
	payloadStr := strings.Repeat("x", cfg.payload)

	// One shared token bucket, so -rate is the AGGREGATE rate however many
	// writers there are. Burst is one batch: the grant granularity is the same
	// round-trip granularity the single-writer loader had, so W writers do not
	// buy themselves a W-batch burst at startup.
	limit := rate.Limit(cfg.rate)
	if cfg.unbounded {
		limit = rate.Inf
	}
	limiter := rate.NewLimiter(limit, cfg.batch)

	conns := make([]*pgx.Conn, cfg.writers)
	for w := range conns {
		c, err := connect(ctx, cfg.url)
		if err != nil {
			return fmt.Errorf("connect (writer %d): %w", w, err)
		}
		conns[w] = c
		defer c.Close(context.Background())
	}

	ws := newWriters(cfg.writers, cfg.tables, cfg.window)
	if err := fillAll(ctx, cfg, ws, conns, tableNames, payloadStr); err != nil {
		return err
	}

	var issuedOps, failedBatches, reconnects atomic.Int64

	// Steady state: W writers, each pipelining -batch statements per round
	// trip, all drawing from the one shared bucket.
	var runWG sync.WaitGroup
	for w := range ws {
		runWG.Add(1)
		go func(w int) {
			defer runWG.Done()
			ws[w].writeLoop(ctx, cfg, limiter, conns[w], tableNames, payloadStr,
				&issuedOps, &failedBatches, &reconnects)
		}(w)
	}

	reportDone := make(chan struct{})
	go func() {
		defer close(reportDone)
		report(ctx, &issuedOps, cfg.rate, cfg.unbounded)
	}()

	runWG.Wait()
	<-reportDone
	log.Printf("loader: stopping after %d issued ops (%d failed batches, %d reconnects)",
		issuedOps.Load(), failedBatches.Load(), reconnects.Load())
	return nil
}

// fillAll fills every writer's slice of every table's initial window, in
// parallel, so UPDATE/DELETE have live rows to hit from the first steady-state
// batch. The union across writers is exactly the id set [0, -window) per table
// for any -writers, so the table the arms capture is independent of W.
func fillAll(ctx context.Context, cfg *config, ws []*writer, conns []*pgx.Conn, tableNames []string, payload string) error {
	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, len(ws))
	for w := range ws {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for t, name := range tableNames {
				if err := ws[w].fill(ctx, conns[w], t, name, payload, cfg.batch); err != nil {
					errs <- fmt.Errorf("fill %s (writer %d): %w", name, w, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return err
	}
	log.Printf("loader: filled %d tables x %d rows across %d writers in %s",
		cfg.tables, cfg.window, cfg.writers, time.Since(start).Round(time.Millisecond))
	return nil
}

// writeLoop is one writer's steady state: take cfg.batch tokens from the
// shared bucket, then issue that many statements in one round trip, until ctx
// is cancelled.
func (w *writer) writeLoop(ctx context.Context, cfg *config, limiter *rate.Limiter, conn *pgx.Conn,
	tableNames []string, payload string, issued, failed, reconnected *atomic.Int64,
) {
	// A deterministic, seeded PRNG is required, not incidental: the statement
	// stream must be reproducible across arms and across runs, and writer 0's
	// seed is the one the pre-writers loader used.
	rng := rand.New(rand.NewSource(baseSeed + w.index)) //nolint:gosec // deterministic by requirement; not security-relevant
	// run() closes the connection it handed over; any replacement this loop
	// opens is this loop's to close.
	owned := (*pgx.Conn)(nil)
	defer func() {
		if owned != nil {
			owned.Close(context.Background())
		}
	}()
	for {
		if err := limiter.WaitN(ctx, cfg.batch); err != nil {
			return // ctx cancelled
		}
		err := w.runBatch(ctx, conn, tableNames, rng, cfg.batch, payload, cfg.insertFrac, cfg.updateFrac)
		issued.Add(int64(cfg.batch))
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		failed.Add(1)
		log.Printf("loader: writer %d batch: %v", w.index, err)
		// A dead connection would otherwise spin this loop hot and silently
		// stop offering load. Reconnect once per failure; a run whose final log
		// reports reconnects had a gap in the offered load and must not be
		// published.
		if conn.IsClosed() {
			c, cerr := connect(ctx, cfg.url)
			if cerr != nil {
				log.Printf("loader: writer %d could not reconnect, stopping: %v", w.index, cerr)
				return
			}
			if owned != nil {
				owned.Close(context.Background())
			}
			conn, owned = c, c
			reconnected.Add(1)
		}
	}
}

// report logs the achieved aggregate rate every reportEvery until ctx is done.
// A window that delivers less than underrunFrac of the offered rate is called
// out as an UNDERRUN: the loader was the limiter, so no arm measured in that
// window measured its own capacity.
func report(ctx context.Context, issued *atomic.Int64, target int, unbounded bool) {
	ticker := time.NewTicker(reportEvery)
	defer ticker.Stop()
	last, lastOps := time.Now(), issued.Load()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			ops := issued.Load()
			dt := now.Sub(last).Seconds()
			if dt <= 0 {
				continue
			}
			achieved := float64(ops-lastOps) / dt
			if unbounded {
				log.Printf("loader: %.0f ops/s (unbounded), total %d", achieved, ops)
			} else {
				log.Printf("loader: %.0f ops/s (target %d), total %d", achieved, target, ops)
				if achieved < underrunFrac*float64(target) {
					log.Printf("loader: UNDERRUN: delivered %.0f of %d offered ops/s. The LOADER is the limiter in this window; raise -writers or treat every arm measured here as loader-limited (README.md, \"The ceiling rule\").", achieved, target)
				}
			}
			last, lastOps = now, ops
		}
	}
}

// window is one writer's live rows in one table, as a dense half-open range of
// SLOT indices (not ids). Slot k of writer w is id w+k*W; see (*writer).id.
type window struct {
	low, high int64
}

// writer owns, in every table, exactly the ids congruent to index modulo
// stride. Two writers of the same loader therefore never touch the same row:
// UPDATE and DELETE pick a slot from this writer's own dense live range, so
// their target is deterministic and uncontended.
type writer struct {
	index  int64
	stride int64
	wins   []window
}

// id maps one of this writer's slot indices to the table id it owns.
//
// Invariant: for writers w != v of the same loader, id ranges are disjoint —
// w.id(k) ≡ w.index and v.id(k) ≡ v.index (mod stride), and the indices differ.
func (w *writer) id(slot int64) int64 { return w.index + slot*w.stride }

// newWriters builds n writers over t tables, splitting an initial window of
// `win` rows per table across them. Writer w starts with slots [0, share) where
// share = writerShare(win, n, w), so the union of the writers' initial ids is
// exactly [0, win) for any n (see writerShare).
//
// win must be >= n, or some writer gets an empty window and its first UPDATE
// has no live row to pick; (*config).validate rejects that at startup.
func newWriters(n, t, win int) []*writer {
	ws := make([]*writer, n)
	for w := range ws {
		wins := make([]window, t)
		share := writerShare(win, n, w)
		for i := range wins {
			wins[i] = window{low: 0, high: share}
		}
		ws[w] = &writer{index: int64(w), stride: int64(n), wins: wins}
	}
	return ws
}

// writerShare returns how many of the `win` initial rows per table writer w of
// n owns. The remainder is handed to the lowest-numbered writers, which is
// exactly what makes the union of {w + k*n : k < share(w)} over all w equal
// [0, win): writer w's largest id is w + (share(w)-1)*n, and giving the first
// win%n writers one extra slot fills the final partial stride left to right.
func writerShare(win, n, w int) int64 {
	share := int64(win / n)
	if w < win%n {
		share++
	}
	return share
}

// runBatch issues `n` statements in one pgx.Batch round trip, round-robin
// across the tables so this writer's share of the rate is split evenly between
// them (not multiplied).
func (w *writer) runBatch(ctx context.Context, conn *pgx.Conn, tableNames []string, rng *rand.Rand, n int, payload string, insertFrac, updateFrac float64) error {
	b := &pgx.Batch{}
	for i := 0; i < n; i++ {
		t := i % len(w.wins) // round-robin across tables
		table := tableNames[t]
		switch o := w.nextOp(t, rng, insertFrac, updateFrac); o.kind {
		case opInsert:
			b.Queue("INSERT INTO "+table+" (id, payload) VALUES ($1, $2)", o.id, payload)
		case opUpdate:
			b.Queue("UPDATE "+table+" SET payload = $2 WHERE id = $1", o.id, payload)
		case opDelete:
			b.Queue("DELETE FROM "+table+" WHERE id = $1", o.id)
		}
	}
	br := conn.SendBatch(ctx, b)
	defer br.Close()
	for i := 0; i < b.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

type opKind int

const (
	opInsert opKind = iota
	opUpdate
	opDelete
)

// op is one statement's worth of intent: what to do, and to which id.
type op struct {
	kind opKind
	id   int64
}

// nextOp advances this writer's window for table t and returns the operation to
// issue. The 70/20/10 INSERT/UPDATE/DELETE mix, the "update a random live row"
// and "delete the oldest live row" choices, and the "always keep at least one
// live row" fallback are the measured workload and must not change: they are
// what makes the committed results comparable.
func (w *writer) nextOp(t int, rng *rand.Rand, insertFrac, updateFrac float64) op {
	win := &w.wins[t]
	roll := rng.Float64()
	switch {
	case roll < insertFrac:
		id := w.id(win.high)
		win.high++
		return op{kind: opInsert, id: id}
	case roll < insertFrac+updateFrac:
		slot := win.low + rng.Int63n(win.high-win.low)
		return op{kind: opUpdate, id: w.id(slot)}
	default:
		if win.high-win.low > 1 { // always keep at least one live row
			id := w.id(win.low)
			win.low++
			return op{kind: opDelete, id: id}
		}
		id := w.id(win.high)
		win.high++
		return op{kind: opInsert, id: id}
	}
}

// fill inserts this writer's share of table t's initial window — its slots
// [0, high) as ids — in batches of `batch`.
func (w *writer) fill(ctx context.Context, conn *pgx.Conn, t int, table, payload string, batch int) error {
	count := w.wins[t].high
	for start := int64(0); start < count; start += int64(batch) {
		end := min(start+int64(batch), count)
		b := &pgx.Batch{}
		for slot := start; slot < end; slot++ {
			b.Queue("INSERT INTO "+table+" (id, payload) VALUES ($1, $2)", w.id(slot), payload)
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
	}
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
		if attempt < 5 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
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
// until timeout or ctx cancellation. It returns nil once all slots are present
// and a descriptive error on timeout/cancellation. This is the slot-aware
// replacement for the old blind -delay: the loader must not write before the
// snapshotMode=never CDC source has created its slot, or pre-slot rows become
// phantom upsert-INSERTs that inflate a Postgres-destination arm's row count.
func waitForSlots(ctx context.Context, conn *pgx.Conn, slots []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastMissing := slots // if the first poll errors, report the whole wanted set
	for {
		present, err := replicationSlots(ctx, conn)
		if err != nil {
			// The poll itself is best-effort (e.g. a transient reconnect); the
			// deadline, not the first error, decides failure.
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

// replicationSlots returns the set of slot names currently in
// pg_replication_slots.
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

// missingSlots returns the subset of want that is absent from present, in
// want's order.
func missingSlots(want []string, present map[string]bool) []string {
	var missing []string
	for _, s := range want {
		if !present[s] {
			missing = append(missing, s)
		}
	}
	return missing
}
