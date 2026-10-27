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
//	{"ts_ms":..., "src_events":..., "src_net":..., "step":..., "target":...,
//	 "sinks":{"<spec>":{"count":..., "lat_n":..., "lat_p50_us":..., ...}}}
//
// src_* is the loader's committed progress (the offered rate it achieved);
// each sink's count is the ground truth read back from the sink. Every rate,
// lag, step verdict and capacity in the results is computed from these rows
// offline (matrix/analyze.py), so the raw series is the record of the run.
//
// Latency is sampled every -latency-every (it reads data, counts do not):
// Kafka from the tail of each partition against broker append time, Postgres
// from the landed_at trigger column, HTTP from the receiver's arrival clock,
// S3 from object LastModified (one-second resolution). File sinks have no
// arrival timestamp, so no latency is reported for them.
func runSample(args []string) error {
	fs := flag.NewFlagSet("sample", flag.ExitOnError)
	var (
		out          = fs.String("out", "/out/samples.jsonl", "output file (JSON lines)")
		interval     = fs.Duration("interval", 5*time.Second, "count interval")
		latEvery     = fs.Duration("latency-every", 30*time.Second, "latency interval (0 disables latency)")
		progressURL  = fs.String("progress", "http://cdcparity-loader:9100/progress", "loader progress endpoint (empty = no source)")
		sinks        = fs.String("sinks", "", "comma-separated sink specs (see sinks.go)")
		pgURL        = fs.String("pg", "postgres://bench:bench@cdcparity-postgres:5432/bench?sslmode=disable", "Postgres URL for pg sinks")
		pgLat        = fs.Bool("pg-latency", false, "pg sinks have the landed_at trigger column")
		brokers      = fs.String("brokers", "cdcparity-kafka:9092", "Kafka bootstrap servers for kafka sinks")
		s3URL        = fs.String("s3", "http://cdcparity-minio:9000", "S3 endpoint for s3 sinks")
		tables       = fs.Int("tables", 1, "number of source tables (pg sink table count)")
		kafkaTailLen = fs.Int64("kafka-tail", 2000, "records per partition read for Kafka latency")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	specs, err := parseSinks(*sinks)
	if err != nil {
		return err
	}
	e := env{pgURL: *pgURL, brokers: *brokers, s3URL: *s3URL, s3Key: "conduitminio", s3Sec: "conduitminiosecret", tables: *tables}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	f, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)

	type reader struct {
		spec  sinkSpec
		count func(context.Context, bool) (map[string]any, error)
	}
	var readers []reader
	for _, s := range specs {
		s := s
		var fn func(context.Context, bool) (map[string]any, error)
		switch s.Kind {
		case "file":
			fc := newFileCounter(s.Arg)
			fn = func(context.Context, bool) (map[string]any, error) {
				n, err := fc.count()
				return map[string]any{"count": n}, err
			}
		case "pg":
			var conn *pgx.Conn
			tbls := pgTables(s.Arg, *tables)
			since := time.Unix(0, 0)
			fn = func(ctx context.Context, lat bool) (map[string]any, error) {
				if conn == nil || conn.IsClosed() {
					c, err := pgx.Connect(ctx, e.pgURL)
					if err != nil {
						return nil, err
					}
					conn = c
				}
				n, err := pgCount(ctx, conn, tbls)
				if err != nil {
					conn.Close(ctx)
					conn = nil
					return nil, err
				}
				res := map[string]any{"count": n}
				if lat && *pgLat {
					l, last, err := pgLatency(ctx, conn, tbls, since)
					if err == nil {
						since = last
						for k, v := range l {
							res[k] = v
						}
					}
				}
				return res, nil
			}
		case "kafka":
			cl, err := kgo.NewClient(kgo.SeedBrokers(e.brokers))
			if err != nil {
				return err
			}
			defer cl.Close()
			adm := kadm.NewClient(cl)
			fn = func(ctx context.Context, lat bool) (map[string]any, error) {
				n, err := kafkaCount(ctx, adm, s.Arg)
				if err != nil {
					return nil, err
				}
				res := map[string]any{"count": n}
				if lat {
					if l, err := kafkaTailLatency(ctx, e, adm, s.Arg, *kafkaTailLen); err == nil {
						for k, v := range l {
							res[k] = v
						}
					}
				}
				return res, nil
			}
		case "s3":
			cl, err := s3Client(e)
			if err != nil {
				return err
			}
			sc := &s3Counter{cl: cl, bucket: s.Arg, seen: map[string]bool{}}
			var pending []int64
			fn = func(ctx context.Context, lat bool) (map[string]any, error) {
				n, l, err := sc.count(ctx)
				pending = append(pending, l...)
				res := map[string]any{"count": n}
				if lat {
					for k, v := range percentiles(pending) {
						res[k] = v
					}
					pending = nil
				}
				return res, err
			}
		case "http":
			fn = func(ctx context.Context, _ bool) (map[string]any, error) {
				m, err := httpStats(ctx, s.Arg)
				if err != nil {
					return nil, err
				}
				return m, nil
			}
		}
		readers = append(readers, reader{s, fn})
	}

	log.Printf("sample: writing %s every %s (latency every %s), sinks %v", *out, *interval, *latEvery, specs)
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	nextLat := time.Now().Add(*latEvery)
	for {
		row := map[string]any{"ts_ms": time.Now().UnixMilli()}
		if *progressURL != "" {
			if p, err := getJSON(ctx, *progressURL); err == nil {
				row["src_events"], row["src_net"], row["step"], row["target"], row["src_done"] = p["events"], p["net"], p["step"], p["target"], p["done"]
			} else {
				row["src_err"] = err.Error()
			}
		}
		lat := *latEvery > 0 && time.Now().After(nextLat)
		if lat {
			nextLat = time.Now().Add(*latEvery)
		}
		sm := map[string]any{}
		for _, r := range readers {
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			res, err := r.count(cctx, lat)
			cancel()
			if err != nil {
				res = map[string]any{"err": err.Error()}
			}
			sm[r.spec.String()] = res
		}
		row["sinks"] = sm
		row["ts_end_ms"] = time.Now().UnixMilli()
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
