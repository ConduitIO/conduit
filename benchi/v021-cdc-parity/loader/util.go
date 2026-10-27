package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Small helpers the benchi hooks call, so the hook containers need nothing but
// this binary (no apk installs racing the run).

// runNow prints KEY=<unix ms>, for timestamps in benchi.log.
func runNow(args []string) error {
	key := "NOW_MS"
	if len(args) > 0 {
		key = args[0]
	}
	fmt.Printf("%s=%d\n", key, time.Now().UnixMilli())
	return nil
}

// runWaitFile waits until a file exists (a fill's -done-file).
func runWaitFile(args []string) error {
	fs := flag.NewFlagSet("wait-file", flag.ExitOnError)
	path := fs.String("path", "", "file to wait for")
	timeout := fs.Duration("timeout", 30*time.Minute, "give up after")
	if err := fs.Parse(args); err != nil {
		return err
	}
	deadline := time.Now().Add(*timeout)
	for {
		if _, err := os.Stat(*path); err == nil {
			fmt.Printf("WAITED_MS=%d\n", time.Now().UnixMilli())
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not appear within %s", *path, *timeout)
		}
		time.Sleep(time.Second)
	}
}

// runMkBucket creates the S3 bucket the S3 sinks write to.
func runMkBucket(args []string) error {
	fs := flag.NewFlagSet("mkbucket", flag.ExitOnError)
	s3URL := fs.String("s3", "http://cdcparity-minio:9000", "S3 endpoint")
	bucket := fs.String("bucket", "bench", "bucket")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cl, err := s3Client(env{s3URL: *s3URL, s3Key: "conduitminio", s3Sec: "conduitminiosecret"})
	if err != nil {
		return err
	}
	ctx := context.Background()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		err = cl.MakeBucket(ctx, *bucket, minio.MakeBucketOptions{Region: "us-east-1"})
		if err == nil {
			return nil
		}
		if ok, _ := cl.BucketExists(ctx, *bucket); ok {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Second)
	}
}

// runFirst waits until the sinks hold a record and prints FIRST_MS, or, with
// -until, until their summed count reaches it and has been stable for
// -stable (the snapshot and correctness runs: "fully landed"), printing
// DONE_MS and DONE_COUNT.
func runFirst(args []string) error {
	fs := flag.NewFlagSet("first", flag.ExitOnError)
	var (
		sinks   = fs.String("sinks", "", "sink specs")
		until   = fs.Int64("until", 0, "wait for this summed count instead of the first record (0 = first record)")
		stable  = fs.Duration("stable", 30*time.Second, "with -until 0 and -settle: how long the count must not move")
		settle  = fs.Bool("settle", false, "wait until the count stops moving (correctness runs: the loader is done)")
		timeout = fs.Duration("timeout", 15*time.Minute, "give up after")
		tables  = fs.Int("tables", 1, "source tables (pg sinks)")
		pgURL   = fs.String("pg", "postgres://bench:bench@cdcparity-postgres:5432/bench?sslmode=disable", "Postgres URL")
		brokers = fs.String("brokers", "cdcparity-kafka:9092", "Kafka")
		s3URL   = fs.String("s3", "http://cdcparity-minio:9000", "S3 endpoint")
		done    = fs.String("progress", "", "with -settle: loader progress URL that must report done first")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	specs, err := parseSinks(*sinks)
	if err != nil {
		return err
	}
	e := env{pgURL: *pgURL, brokers: *brokers, s3URL: *s3URL, s3Key: "conduitminio", s3Sec: "conduitminiosecret", tables: *tables}
	ctx := context.Background()
	counters := make([]func() (int64, error), 0, len(specs))
	for _, s := range specs {
		s := s
		switch s.Kind {
		case "file":
			fc := newFileCounter(s.Arg)
			counters = append(counters, fc.count)
		case "pg":
			counters = append(counters, func() (int64, error) {
				c, err := pgx.Connect(ctx, e.pgURL)
				if err != nil {
					return 0, err
				}
				defer c.Close(ctx)
				return pgCount(ctx, c, pgTables(s.Arg, e.tables))
			})
		case "kafka":
			cl, err := kgo.NewClient(kgo.SeedBrokers(e.brokers))
			if err != nil {
				return err
			}
			defer cl.Close()
			adm := kadm.NewClient(cl)
			counters = append(counters, func() (int64, error) { return kafkaCount(ctx, adm, s.Arg) })
		case "s3":
			cl, err := s3Client(e)
			if err != nil {
				return err
			}
			sc := &s3Counter{cl: cl, bucket: s.Arg, seen: map[string]bool{}}
			counters = append(counters, func() (int64, error) { n, _, err := sc.count(ctx); return n, err })
		case "http":
			counters = append(counters, func() (int64, error) {
				m, err := httpStats(ctx, s.Arg)
				if err != nil {
					return 0, err
				}
				f, _ := m["count"].(float64)
				return int64(f), nil
			})
		}
	}
	// min over sinks: a fan-out pipeline has landed only when every sink has.
	count := func() int64 {
		var lo int64 = -1
		for _, c := range counters {
			n, err := c()
			if err != nil {
				n = 0
			}
			if lo < 0 || n < lo {
				lo = n
			}
		}
		if lo < 0 {
			return 0
		}
		return lo
	}
	deadline := time.Now().Add(*timeout)
	interval := 500 * time.Millisecond
	if *until > 0 || *settle {
		interval = 2 * time.Second
	}
	var last int64 = -1
	lastMove := time.Now()
	for {
		n := count()
		now := time.Now()
		switch {
		case *until == 0 && !*settle && n > 0:
			fmt.Printf("FIRST_MS=%d\n", now.UnixMilli())
			return nil
		case *until > 0 && n >= *until:
			fmt.Printf("DONE_MS=%d\nDONE_COUNT=%d\n", now.UnixMilli(), n)
			return nil
		case *settle:
			if n != last {
				last, lastMove = n, now
			} else if now.Sub(lastMove) >= *stable && (*done == "" || loaderDone(ctx, *done)) {
				fmt.Printf("DONE_MS=%d\nDONE_COUNT=%d\n", lastMove.UnixMilli(), n)
				return nil
			}
		}
		if now.After(deadline) {
			fmt.Printf("TIMEOUT_MS=%d\nTIMEOUT_COUNT=%d\n", now.UnixMilli(), n)
			return fmt.Errorf("sinks did not reach the condition within %s (count %d)", *timeout, n)
		}
		time.Sleep(interval)
	}
}

func loaderDone(ctx context.Context, url string) bool {
	p, err := getJSON(ctx, url)
	if err != nil {
		return false
	}
	d, _ := p["done"].(bool)
	return d
}
