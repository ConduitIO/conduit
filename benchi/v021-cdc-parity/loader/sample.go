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
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// sample writes one JSON object per interval to -out:
//
//	{"ts_ms":..., "src":{"t_ms":..., "events":..., "net":..., "step":..., "target":...},
//	 "sinks":{"<spec>":{"t_ms":..., "count":...}}, "slots":{"<slot>":lag_bytes}}
//
// Every reading carries its own t_ms, the midpoint of the read, so a slow
// read cannot skew a rate: analysis divides count deltas by the reading's own
// timestamps, never by the row's.
//
// What sample does NOT do, on purpose, because it would load the measured box
// in a way that depends on the arm:
//   - no count(*) on a destination table: Postgres progress is one index probe
//     per table (pgPosition);
//   - no S3 reads: objects are indexed after the run (collect s3-index), and
//     the count series is rebuilt from LastModified;
//   - no latency reads: latency comes from exact histograms built after the
//     run (collect kafka-hist, pg-hist, http-hist).
func runSample(args []string) error {
	fs := flag.NewFlagSet("sample", flag.ExitOnError)
	var (
		out         = fs.String("out", "/out/samples.jsonl", "output file (JSON lines)")
		interval    = fs.Duration("interval", 5*time.Second, "sample interval")
		progressURL = fs.String("progress", "http://cdcparity-loader:9100/progress", "loader progress endpoint (empty = no source)")
		sinks       = fs.String("sinks", "", "comma-separated sink specs (see sinks.go)")
		pgURL       = fs.String("pg", "postgres://bench:bench@cdcparity-postgres:5432/bench?sslmode=disable", "Postgres URL")
		slots       = fs.Bool("slots", false, "sample replication slot lag (Postgres sources)")
		brokers     = fs.String("brokers", "cdcparity-kafka:9092", "Kafka bootstrap servers for kafka sinks")
		tables      = fs.Int("tables", 1, "number of destination tables (pg sinks)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	specs, err := parseSinks(*sinks)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	f, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)

	var pg *pgx.Conn
	pgConn := func(ctx context.Context) (*pgx.Conn, error) {
		if pg == nil || pg.IsClosed() {
			c, err := pgx.Connect(ctx, *pgURL)
			if err != nil {
				return nil, err
			}
			pg = c
		}
		return pg, nil
	}

	type reader struct {
		spec sinkSpec
		read func(context.Context) (int64, error)
	}
	var readers []reader
	for _, s := range specs {
		s := s
		var fn func(context.Context) (int64, error)
		switch s.Kind {
		case "file":
			fc := newFileCounter(s.Arg)
			fn = func(context.Context) (int64, error) { return fc.count() }
		case "pg", "pgins", "pgops":
			tbls := pgTables(s.Arg, *tables)
			fn = func(ctx context.Context) (int64, error) {
				c, err := pgConn(ctx)
				if err != nil {
					return 0, err
				}
				switch s.Kind {
				case "pgins":
					return pgInserts(ctx, c, tbls)
				case "pgops":
					return pgOps(ctx, c, tbls)
				}
				return pgPosition(ctx, c, tbls)
			}
		case "kafka":
			cl, err := kgo.NewClient(kgo.SeedBrokers(*brokers))
			if err != nil {
				return err
			}
			defer cl.Close()
			adm := kadm.NewClient(cl)
			fn = func(ctx context.Context) (int64, error) { return kafkaCount(ctx, adm, s.Arg) }
		case "s3":
			continue // indexed after the run
		case "http":
			fn = func(ctx context.Context) (int64, error) {
				m, err := httpStats(ctx, s.Arg)
				if err != nil {
					return 0, err
				}
				n, _ := m["count"].(float64)
				return int64(n), nil
			}
		}
		readers = append(readers, reader{s, fn})
	}

	log.Printf("sample: writing %s every %s, sinks %v", *out, *interval, specs)
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for {
		row := map[string]any{"ts_ms": time.Now().UnixMilli()}
		if *progressURL != "" {
			t0 := time.Now()
			p, err := getJSON(ctx, *progressURL)
			src := map[string]any{"t_ms": mid(t0)}
			if err == nil {
				src["events"], src["net"], src["step"], src["target"], src["done"] = p["events"], p["net"], p["step"], p["target"], p["done"]
			} else {
				src["err"] = err.Error()
			}
			row["src"] = src
		}
		sm := map[string]any{}
		for _, r := range readers {
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			t0 := time.Now()
			n, err := r.read(cctx)
			cancel()
			res := map[string]any{"t_ms": mid(t0), "read_ms": time.Since(t0).Milliseconds()}
			if err != nil {
				res["err"] = err.Error()
				if r.spec.Kind == "pg" || r.spec.Kind == "pgins" || r.spec.Kind == "pgops" {
					if pg != nil {
						pg.Close(ctx)
					}
					pg = nil
				}
			} else {
				res["count"] = n
			}
			sm[r.spec.String()] = res
		}
		row["sinks"] = sm
		if *slots {
			if c, err := pgConn(ctx); err == nil {
				row["slots"] = slotLag(ctx, c)
			}
		}
		if err := enc.Encode(row); err != nil {
			return fmt.Errorf("write sample: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// mid is the midpoint between t0 and now, in unix ms.
func mid(t0 time.Time) int64 {
	return t0.Add(time.Since(t0) / 2).UnixMilli()
}

// slotLag is, per replication slot, the WAL bytes between the server's
// current LSN and what the slot's consumer has confirmed.
func slotLag(ctx context.Context, c *pgx.Conn) map[string]int64 {
	out := map[string]int64{}
	rows, err := c.Query(ctx, "SELECT slot_name, COALESCE(pg_wal_lsn_diff(pg_current_wal_lsn(), confirmed_flush_lsn),0)::bigint FROM pg_replication_slots")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		var v int64
		if rows.Scan(&n, &v) == nil {
			out[n] = v
		}
	}
	return out
}

func getJSON(ctx context.Context, url string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var m map[string]any
	return m, json.NewDecoder(resp.Body).Decode(&m)
}
