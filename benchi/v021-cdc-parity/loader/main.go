// Command loader writes a steady, fixed-rate mix of INSERT/UPDATE/DELETE
// against the Postgres source table that all three CDC systems under test
// capture. It is the SAME loader, at the SAME rate, for every arm — the
// workload is identical by construction; only the CDC pipeline differs.
//
// The loader keeps a sliding window of live rows [low, high) and cycles:
//   - INSERT (default 70%): a new row with the next id
//   - UPDATE (default 20%): rewrite the payload of a random live row
//   - DELETE (default 10%): remove the oldest live row (log-retention style)
//
// The table therefore GROWS at (insert-delete) rate — an append-heavy,
// OLTP-like workload — while a continuous stream of all three operation
// kinds flows through the WAL. Updates and deletes always target a live row
// (ids in [low, high) are dense). Row shape (id bigint PRIMARY KEY,
// payload text of -payload bytes) is fixed, so the WAL volume per operation
// is identical across arms.
//
// Statements are sent through pgx.Batch (one round trip per -batch
// statements, not one per statement), so the loader's ceiling is Postgres's
// WAL write throughput, far above any single CDC pipeline's capture rate.
// That matters: the loader must sustain MORE operations per second than the
// fastest CDC arm can capture, so every arm is measured at its own capture
// ceiling rather than at the loader's rate. If an arm's measured throughput
// equals -rate, the loader was the bottleneck and -rate must be raised (see
// README.md). A counter is logged every 5s so the achieved rate can be
// checked against -rate in the run logs.
package main

import (
	"context"
	"flag"
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
		rate       = flag.Int("rate", 20000, "target operations per second")
		window     = flag.Int("window", 10000, "initial number of live rows")
		payload    = flag.Int("payload", 64, "payload size in bytes")
		batch      = flag.Int("batch", 1000, "statements per round trip")
		insertFrac = flag.Float64("insert-frac", 0.70, "fraction of INSERT operations")
		updateFrac = flag.Float64("update-frac", 0.20, "fraction of UPDATE operations")
	)
	flag.Parse()
	if *url == "" {
		log.Fatal("-url is required")
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

	log.Printf("loader: rate=%d ops/s window=%d payload=%d bytes batch=%d insert=%.0f%% update=%.0f%% delete=%.0f%%",
		*rate, *window, *payload, *batch, *insertFrac*100, *updateFrac*100, (1-*insertFrac-*updateFrac)*100)

	payloadStr := strings.Repeat("x", *payload)

	// Fill the initial window (also batched), so UPDATE/DELETE have live rows
	// to hit from the first steady-state batch.
	fillStart := time.Now()
	if err := fill(ctx, conn, int64(*window), payloadStr, *batch); err != nil {
		log.Fatalf("fill: %v", err)
	}
	log.Printf("loader: filled window of %d rows in %s", *window, time.Since(fillStart).Round(time.Millisecond))

	state := &loaderState{low: 0, high: int64(*window)}
	rng := rand.New(rand.NewSource(0xC0FFEE))

	period := time.Second / time.Duration(*rate)
	var (
		totalOps  int64
		logEvery  = time.Now().Add(5 * time.Second)
		logAtOps  int64
	)

	for {
		select {
		case <-ctx.Done():
			log.Printf("loader: stopping after %d total ops (window [%d,%d))", totalOps, state.low, state.high)
			return
		default:
		}

		start := time.Now()
		if err := runBatch(ctx, conn, state, rng, *batch, payloadStr, *insertFrac, *updateFrac); err != nil {
			log.Printf("batch: %v", err)
		}
		totalOps += int64(*batch)

		// Maintain the fixed rate at batch granularity.
		if elapsed := time.Since(start); elapsed < time.Duration(*batch)*period {
			time.Sleep(time.Duration(*batch)*period - elapsed)
		}

		if now := time.Now(); now.After(logEvery) {
			dt := now.Sub(logEvery.Add(-5 * time.Second)).Seconds()
			log.Printf("loader: %d ops/s (target %d), window [%d,%d), total %d",
				int64(float64(totalOps-logAtOps)/dt), *rate, state.low, state.high, totalOps)
			logAtOps = totalOps
			logEvery = now.Add(5 * time.Second)
		}
	}
}

type loaderState struct {
	low, high int64
}

// runBatch issues `n` statements in one pgx.Batch round trip.
func runBatch(ctx context.Context, conn *pgx.Conn, st *loaderState, rng *rand.Rand, n int, payload string, insertFrac, updateFrac float64) error {
	b := &pgx.Batch{}
	for i := 0; i < n; i++ {
		roll := rng.Float64()
		switch {
		case roll < insertFrac:
			b.Queue("INSERT INTO bench_source (id, payload) VALUES ($1, $2)", st.high, payload)
			st.high++
		case roll < insertFrac+updateFrac:
			id := st.low + rng.Int63n(st.high-st.low)
			b.Queue("UPDATE bench_source SET payload = $2 WHERE id = $1", id, payload)
		default:
			if st.high-st.low > 1 { // always keep at least one live row
				b.Queue("DELETE FROM bench_source WHERE id = $1", st.low)
				st.low++
			} else {
				b.Queue("INSERT INTO bench_source (id, payload) VALUES ($1, $2)", st.high, payload)
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

// fill inserts ids [0, count) in batches.
func fill(ctx context.Context, conn *pgx.Conn, count int64, payload string, batch int) error {
	for start := int64(0); start < count; start += int64(batch) {
		end := start + int64(batch)
		if end > count {
			end = count
		}
		b := &pgx.Batch{}
		for id := start; id < end; id++ {
			b.Queue("INSERT INTO bench_source (id, payload) VALUES ($1, $2)", id, payload)
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
