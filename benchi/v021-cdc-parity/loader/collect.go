package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/twmb/franz-go/pkg/kgo"
)

// collect builds, AFTER a run's measured window, the data that would cost the
// measured box something if it were read during the window:
//
//	collect kafka-hist -topics RE -out F   every record of the topics: per
//	                                       second of broker append time, count,
//	                                       bytes and the exact latency histogram
//	                                       (append time minus payload marker)
//	collect pg-hist -tables PAT -n N -out F per second of landed_at (the
//	                                       latency trigger, ramp cells), count
//	                                       and latency histogram of rows present
//	collect http-hist -url U -out F        the receiver's per-second histograms
//	collect s3-index -bucket B -out F      per object: key, LastModified, size,
//	                                       lines, latency histogram (1 s
//	                                       resolution: S3 times are whole seconds)
//	collect pgstat -out F                  pg_stat_statements (calls, rows, time
//	                                       per statement) and pg_stat_user_tables
//
// Each writes JSON lines; analyze.py merges them by second into windows.
func runCollect(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("collect: kafka-hist | pg-hist | http-hist | s3-index | pgstat")
	}
	what, args := args[0], args[1:]
	fs := flag.NewFlagSet("collect "+what, flag.ExitOnError)
	var (
		out     = fs.String("out", "", "output file")
		topics  = fs.String("topics", "", "topic regex (kafka-hist)")
		tables  = fs.String("tables", "bench_dest_%d", "table pattern (pg-hist)")
		n       = fs.Int("n", 1, "table count (pg-hist)")
		url     = fs.String("url", "http://cdcparity-receiver:8090", "receiver (http-hist)")
		bucket  = fs.String("bucket", "bench", "bucket (s3-index)")
		pgURL   = fs.String("pg", "postgres://bench:bench@cdcparity-postgres:5432/bench?sslmode=disable", "Postgres URL")
		brokers = fs.String("brokers", "cdcparity-kafka:9092", "Kafka")
		s3URL   = fs.String("s3", "http://cdcparity-minio:9000", "S3 endpoint")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	e := env{pgURL: *pgURL, brokers: *brokers, s3URL: *s3URL, s3Key: "conduitminio", s3Sec: "conduitminiosecret"}
	ctx := context.Background()
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	enc := json.NewEncoder(w)
	writeSecs := func(s secHists, extra map[string]any) error {
		secs := make([]int64, 0, len(s))
		for k := range s {
			secs = append(secs, k)
		}
		sort.Slice(secs, func(i, j int) bool { return secs[i] < secs[j] })
		for _, k := range secs {
			row := map[string]any{"sec": k, "n": s[k].N, "bytes": s[k].Bytes, "lat": s[k].Lat, "no_marker": s[k].NoMk}
			for a, b := range extra {
				row[a] = b
			}
			if err := enc.Encode(row); err != nil {
				return err
			}
		}
		return nil
	}
	switch what {
	case "kafka-hist":
		s := secHists{}
		batches := map[string]int64{}
		err := eachKafkaRaw(ctx, e, *topics, func(_ string, r *kgo.Record) {
			var ts int64
			if ms := findMarkers(string(r.Value)); len(ms) > 0 {
				ts = ms[len(ms)-1].TS
			}
			s.add(r.Timestamp.UnixMilli(), len(r.Key)+len(r.Value), ts)
			batches[fmt.Sprintf("%s/%d", r.Topic, r.Partition)]++
		})
		if err != nil {
			return err
		}
		return writeSecs(s, nil)
	case "pg-hist":
		conn, err := pgx.Connect(ctx, e.pgURL)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		s := secHists{}
		for _, t := range pgTables(*tables, *n) {
			rows, err := conn.Query(ctx, "SELECT (extract(epoch from landed_at)*1000)::bigint, payload FROM "+pgIdent(t)+" WHERE landed_at IS NOT NULL")
			if err != nil {
				return err
			}
			for rows.Next() {
				var ms int64
				var p string
				if err := rows.Scan(&ms, &p); err != nil {
					rows.Close()
					return err
				}
				var ts int64
				if m := findMarkers(p); len(m) > 0 {
					ts = m[0].TS
				}
				s.add(ms, len(p), ts)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return writeSecs(s, nil)
	case "http-hist":
		m, err := getJSON(ctx, *url+"/hist")
		if err != nil {
			return err
		}
		return enc.Encode(m)
	case "s3-index":
		cl, err := s3Client(e)
		if err != nil {
			return err
		}
		for obj := range cl.ListObjects(ctx, *bucket, minio.ListObjectsOptions{Recursive: true}) {
			if obj.Err != nil {
				return obj.Err
			}
			r, err := cl.GetObject(ctx, *bucket, obj.Key, minio.GetObjectOptions{})
			if err != nil {
				return err
			}
			sc := bufio.NewScanner(r)
			sc.Buffer(make([]byte, 1<<20), 64<<20)
			lines := int64(0)
			lat := hist{}
			lm := obj.LastModified.UnixMilli()
			for sc.Scan() {
				lines++
				if ms := findMarkers(sc.Text()); len(ms) > 0 {
					lat.add(lm*1000 - ms[len(ms)-1].TS/1000)
				}
			}
			r.Close()
			if err := sc.Err(); err != nil {
				return err
			}
			if err := enc.Encode(map[string]any{"key": obj.Key, "lastmod_ms": lm, "size": obj.Size, "lines": lines, "lat": lat}); err != nil {
				return err
			}
		}
		return nil
	case "pgstat":
		conn, err := pgx.Connect(ctx, e.pgURL)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		res := map[string]any{}
		for name, q := range map[string]string{
			"statements": `SELECT json_agg(t) FROM (SELECT left(query, 300) AS query, calls, rows, total_exec_time, mean_exec_time, rows::float8/greatest(calls,1) AS rows_per_call
			                FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 40) t`,
			"tables": `SELECT json_agg(t) FROM (SELECT relname, n_tup_ins, n_tup_upd, n_tup_del, n_tup_hot_upd, n_live_tup, n_dead_tup, autovacuum_count, autoanalyze_count FROM pg_stat_user_tables) t`,
			"wal":    `SELECT row_to_json(t) FROM (SELECT * FROM pg_stat_wal) t`,
		} {
			var raw []byte
			if err := conn.QueryRow(ctx, q).Scan(&raw); err != nil {
				res[name] = map[string]string{"error": err.Error()}
				continue
			}
			res[name] = json.RawMessage(raw)
		}
		return enc.Encode(res)
	}
	return fmt.Errorf("collect: unknown %q", what)
}
