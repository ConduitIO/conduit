// Command loader writes a steady, fixed-rate mix of INSERT/UPDATE/DELETE
// against the Postgres source tables that all CDC systems under test capture.
// It is the SAME loader, at the SAME rate, for every arm — the workload is
// identical by construction; only the CDC pipeline differs.
//
// The loader keeps, per table, a sliding window of live rows [low, high) and
// cycles:
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
// flows through the WAL. Updates and deletes always target a live row (ids in
// [low, high) are dense). Row shape (id bigint PRIMARY KEY, payload text of
// -payload bytes) is fixed, so the WAL volume per operation is identical
// across arms.
//
// Statements are sent through pgx.Batch (one round trip per -batch statements,
// not one per statement), so the loader's ceiling is Postgres's WAL write
// throughput, far above any single CDC pipeline's capture rate. That matters:
// the loader must sustain MORE operations per second than the fastest CDC arm
// can capture, so every arm is measured at its own capture ceiling rather than
// at the loader's rate. If an arm's measured throughput equals -rate, the
// loader was the bottleneck and -rate must be raised (see README.md). A
// counter is logged every 5s so the achieved rate can be checked against -rate
// in the run logs.
//
// The steady fixed-rate stream is also what makes Conduit source batching
// effective: at -rate 20 000 ops/s a source configured with sdk.batch.size
// 1000 fills a full batch in ~50 ms, so it reads 1000 records per ReadN
// instead of one per Read (the batch-of-one gap this benchmark exists to
// surface).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	var (
		url        = flag.String("url", "", "Postgres connection URL (required)")
		rate       = flag.Int("rate", 20000, "target operations per second (total, across all tables)")
		tables     = flag.Int("tables", 1, "number of source tables (bench_source_0 .. bench_source_N-1); the workload is split, not multiplied")
		window     = flag.Int("window", 10000, "initial number of live rows per table")
		payload    = flag.Int("payload", 64, "payload size in bytes")
		batch      = flag.Int("batch", 1000, "statements per round trip")
		insertFrac = flag.Float64("insert-frac", 0.70, "fraction of INSERT operations")
		updateFrac = flag.Float64("update-frac", 0.20, "fraction of UPDATE operations")
	)
	flag.Parse()
	if *url == "" {
		log.Fatal("-url is required")
	}
	if *tables < 1 {
		log.Fatal("-tables must be >= 1")
	}
	if *insertFrac+*updateFrac > 1.0 {
		log.Fatal("insert-frac + update-frac must be <= 1.0")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	conn, err := connect(ctx, *url)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())

	log.Printf("loader: rate=%d ops/s tables=%d window=%d/table payload=%d bytes batch=%d insert=%.0f%% update=%.0f%% delete=%.0f%%",
		*rate, *tables, *window, *payload, *batch, *insertFrac*100, *updateFrac*100, (1-*insertFrac-*updateFrac)*100)

	tableNames := make([]string, *tables)
	for t := 0; t < *tables; t++ {
		tableNames[t] = fmt.Sprintf("bench_source_%d", t)
	}

	payloadStr := strings.Repeat("x", *payload)

	// Fill each table's initial window (also batched), so UPDATE/DELETE have
	// live rows to hit from the first steady-state batch.
	states := make([]loaderState, *tables)
	fillStart := time.Now()
	for t, name := range tableNames {
		if err := fill(ctx, conn, name, int64(*window), payloadStr, *batch); err != nil {
			log.Fatalf("fill %s: %v", name, err)
		}
		states[t] = loaderState{low: 0, high: int64(*window)}
	}
	log.Printf("loader: filled %d tables x %d rows in %s", *tables, *window, time.Since(fillStart).Round(time.Millisecond))

	rng := rand.New(rand.NewSource(0xC0FFEE))

	period := time.Second / time.Duration(*rate)
	var (
		totalOps int64
		logEvery = time.Now().Add(5 * time.Second)
		logAtOps int64
	)

	for {
		select {
		case <-ctx.Done():
			log.Printf("loader: stopping after %d total ops", totalOps)
			return
		default:
		}

		start := time.Now()
		if err := runBatch(ctx, conn, states, tableNames, rng, *batch, payloadStr, *insertFrac, *updateFrac); err != nil {
			log.Printf("batch: %v", err)
		}
		totalOps += int64(*batch)

		// Maintain the fixed rate at batch granularity.
		if elapsed := time.Since(start); elapsed < time.Duration(*batch)*period {
			time.Sleep(time.Duration(*batch)*period - elapsed)
		}

		if now := time.Now(); now.After(logEvery) {
			dt := now.Sub(logEvery.Add(-5 * time.Second)).Seconds()
			log.Printf("loader: %d ops/s (target %d), total %d",
				int64(float64(totalOps-logAtOps)/dt), *rate, totalOps)
			logAtOps = totalOps
			logEvery = now.Add(5 * time.Second)
		}
	}
}

type loaderState struct {
	low, high int64
}

// runBatch issues `n` statements in one pgx.Batch round trip, round-robin
// across the tables so the fixed rate is split evenly (not multiplied).
func runBatch(ctx context.Context, conn *pgx.Conn, states []loaderState, tableNames []string, rng *rand.Rand, n int, payload string, insertFrac, updateFrac float64) error {
	b := &pgx.Batch{}
	for i := 0; i < n; i++ {
		t := i % len(states) // round-robin across tables
		st := &states[t]
		table := tableNames[t]
		roll := rng.Float64()
		switch {
		case roll < insertFrac:
			b.Queue("INSERT INTO "+table+" (id, payload) VALUES ($1, $2)", st.high, payload)
			st.high++
		case roll < insertFrac+updateFrac:
			id := st.low + rng.Int63n(st.high-st.low)
			b.Queue("UPDATE "+table+" SET payload = $2 WHERE id = $1", id, payload)
		default:
			if st.high-st.low > 1 { // always keep at least one live row
				b.Queue("DELETE FROM "+table+" WHERE id = $1", st.low)
				st.low++
			} else {
				b.Queue("INSERT INTO "+table+" (id, payload) VALUES ($1, $2)", st.high, payload)
				st.high++
			}
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

// fill inserts ids [0, count) into table in batches.
func fill(ctx context.Context, conn *pgx.Conn, table string, count int64, payload string, batch int) error {
	for start := int64(0); start < count; start += int64(batch) {
		end := start + int64(batch)
		if end > count {
			end = count
		}
		b := &pgx.Batch{}
		for id := start; id < end; id++ {
			b.Queue("INSERT INTO "+table+" (id, payload) VALUES ($1, $2)", id, payload)
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
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		return nil, err
	}
}
