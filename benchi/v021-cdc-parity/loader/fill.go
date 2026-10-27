package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// The Kafka-source and file-source cells measure how fast a pipeline DRAINS a
// pre-filled backlog: the backlog is written before the pipeline starts, so
// the measured window is the pipeline alone, not a race with a producer.
//
// fill-kafka is idempotent: a topic that already holds -count records is left
// alone. A drain cell fills its topic once and every run reads it with a fresh
// consumer group (run-cell.sh keeps the broker volume for the cell's runs).
//
// Records are inserts of table 0 with id == seq, so `verify` replays them with
// the generator at -insert-frac 1 -window 0 -tables 1. Their shape is the
// Kafka Connect JSON converter's schema envelope (schemas.enable=true), which
// the JDBC sink needs to find the key; the other sinks pass it through.

const (
	keySchema   = `{"type":"struct","fields":[{"field":"id","type":"int64","optional":false}],"optional":false,"name":"bench.Key"}`
	valueSchema = `{"type":"struct","fields":[{"field":"id","type":"int64","optional":false},{"field":"payload","type":"string","optional":false}],"optional":false,"name":"bench.Value"}`
)

func envelopeKey(id int64) []byte {
	return []byte(`{"schema":` + keySchema + `,"payload":{"id":` + strconv.FormatInt(id, 10) + `}}`)
}

func envelopeValue(id int64, payload string) []byte {
	return []byte(`{"schema":` + valueSchema + `,"payload":{"id":` + strconv.FormatInt(id, 10) + `,"payload":"` + payload + `"}}`)
}

// flatLine is the file-source cells' line: plain JSON, no envelope, because
// both file sources emit the line as an opaque string or byte payload.
func flatLine(id int64, payload string) string {
	return `{"id":` + strconv.FormatInt(id, 10) + `,"payload":"` + payload + `"}`
}

func runFillKafka(args []string) error {
	fs := flag.NewFlagSet("fill-kafka", flag.ExitOnError)
	var (
		brokers    = fs.String("brokers", "cdcparity-kafka:9092", "bootstrap servers")
		topic      = fs.String("topic", "bench.source", "topic to create and fill")
		partitions = fs.Int("partitions", 1, "partitions of the topic")
		count      = fs.Int64("count", 1_000_000, "records to produce")
		payload    = fs.Int("payload", 64, "payload size in bytes")
		wait       = fs.Bool("wait", true, "idle after filling, so the container stays up")
		doneFile   = fs.String("done-file", "", "create this file once the fill is acknowledged")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cl, err := kgo.NewClient(kgo.SeedBrokers(*brokers),
		kgo.DefaultProduceTopic(*topic),
		kgo.ProducerBatchMaxBytes(1<<20),
		kgo.ProducerLinger(5*time.Millisecond),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.MaxBufferedRecords(100_000))
	if err != nil {
		return err
	}
	defer cl.Close()
	if err := waitKafka(ctx, cl); err != nil {
		return err
	}
	adm := kadm.NewClient(cl)
	if n, err := kafkaCount(ctx, adm, "^"+regexp.QuoteMeta(*topic)+"$"); err == nil && n == *count {
		log.Printf("fill-kafka: FILL_DONE topic=%s already holds %d records (reused)", *topic, n)
		if err := touch(*doneFile); err != nil {
			return err
		}
		if *wait {
			<-ctx.Done()
		}
		return nil
	} else if err == nil && n > 0 {
		return fmt.Errorf("topic %s holds %d records, want 0 or %d: refusing to append to a partial backlog", *topic, n, *count)
	}
	if _, err := adm.CreateTopic(ctx, int32(*partitions), 1, nil, *topic); err != nil {
		log.Printf("fill-kafka: create topic %s: %v (continuing; it may exist)", *topic, err)
	}

	gen := newGenerator(genConfig{Tables: 1, Batch: 1, InsertFrac: 1, Seed: 1})
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	start := time.Now()
	var (
		mu     sync.Mutex
		failed error
	)
	for i := int64(0); i < *count && ctx.Err() == nil; i++ {
		o := gen.next()
		r := &kgo.Record{Key: envelopeKey(o.ID), Value: envelopeValue(o.ID, encodePayload(o, *payload, time.Now(), rnd))}
		cl.Produce(ctx, r, func(_ *kgo.Record, err error) {
			if err != nil {
				mu.Lock()
				if failed == nil {
					failed = err
				}
				mu.Unlock()
			}
		})
	}
	if err := cl.Flush(ctx); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if failed != nil {
		return fmt.Errorf("produce: %w", failed)
	}
	el := time.Since(start)
	log.Printf("fill-kafka: FILL_DONE topic=%s partitions=%d count=%d payload=%d in %s (%.0f rec/s)",
		*topic, *partitions, *count, *payload, el.Round(time.Millisecond), float64(*count)/el.Seconds())
	if err := touch(*doneFile); err != nil {
		return err
	}
	if *wait {
		<-ctx.Done()
	}
	return nil
}

func waitKafka(ctx context.Context, cl *kgo.Client) error {
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if err := cl.Ping(ctx); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			return fmt.Errorf("kafka never answered: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func runFillFile(args []string) error {
	fs := flag.NewFlagSet("fill-file", flag.ExitOnError)
	var (
		path     = fs.String("path", "/sink/in_0.txt", "file to write")
		count    = fs.Int64("count", 1_000_000, "lines to write")
		payload  = fs.Int("payload", 64, "payload size in bytes")
		wait     = fs.Bool("wait", true, "idle after filling, so the container stays up")
		doneFile = fs.String("done-file", "", "create this file once the fill is on disk")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// Written to a temporary name and renamed, so a source that opens the
	// path sees either nothing or the whole backlog.
	tmp := *path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	gen := newGenerator(genConfig{Tables: 1, Batch: 1, InsertFrac: 1, Seed: 1})
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	start := time.Now()
	for i := int64(0); i < *count; i++ {
		o := gen.next()
		if _, err := w.WriteString(flatLine(o.ID, encodePayload(o, *payload, time.Now(), rnd)) + "\n"); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o666); err != nil {
		return err
	}
	if err := os.Rename(tmp, *path); err != nil {
		return err
	}
	log.Printf("fill-file: FILL_DONE path=%s count=%d payload=%d in %s", *path, *count, *payload, time.Since(start).Round(time.Millisecond))
	if err := touch(*doneFile); err != nil {
		return err
	}
	if *wait {
		<-ctx.Done()
	}
	return nil
}
