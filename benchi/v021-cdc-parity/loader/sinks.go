package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// A sink spec names where a cell's destination writes and how to read it back:
//
//	file:/sink/out_*.txt        lines across the matching files
//	pg:bench_dest_%d            rows of the tables (one per source table index)
//	kafka:^cdcparitykafka\..*   records across the matching topics
//	s3:bench                    lines across every object in the bucket
//	http:http://cdcparity-receiver:8090
//
// `sample` counts a sink repeatedly; `verify` reads it once, completely.
type sinkSpec struct {
	Kind string
	Arg  string
}

func parseSinks(s string) ([]sinkSpec, error) {
	var out []sinkSpec
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, a, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("sink %q: want kind:arg", part)
		}
		switch k {
		case "file", "pg", "kafka", "s3", "http":
		default:
			return nil, fmt.Errorf("sink %q: unknown kind %q", part, k)
		}
		out = append(out, sinkSpec{k, a})
	}
	return out, nil
}

func (s sinkSpec) String() string { return s.Kind + ":" + s.Arg }

// env holds the endpoints shared by all sink readers.
type env struct {
	pgURL   string
	brokers string
	s3URL   string
	s3Key   string
	s3Sec   string
	tables  int
}

// ---- file

type fileCounter struct {
	glob   string
	offset map[string]int64
	lines  map[string]int64
}

func newFileCounter(glob string) *fileCounter {
	return &fileCounter{glob: glob, offset: map[string]int64{}, lines: map[string]int64{}}
}

// count reads only the bytes appended since the last call.
func (c *fileCounter) count() (int64, error) {
	paths, err := filepath.Glob(c.glob)
	if err != nil {
		return 0, err
	}
	buf := make([]byte, 1<<20)
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		st, err := f.Stat()
		if err == nil && st.Size() < c.offset[p] { // truncated or replaced
			c.offset[p], c.lines[p] = 0, 0
		}
		if _, err := f.Seek(c.offset[p], io.SeekStart); err == nil {
			for {
				n, err := f.Read(buf)
				for _, b := range buf[:n] {
					if b == '\n' {
						c.lines[p]++
					}
				}
				c.offset[p] += int64(n)
				if err != nil {
					break
				}
			}
		}
		f.Close()
	}
	var sum int64
	for _, n := range c.lines {
		sum += n
	}
	return sum, nil
}

func eachFileLine(glob string, fn func(source, line string)) error {
	paths, err := filepath.Glob(glob)
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			fn(p, sc.Text())
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

// ---- postgres

func pgTables(pattern string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf(pattern, i)
	}
	return out
}

func pgCount(ctx context.Context, conn *pgx.Conn, tables []string) (int64, error) {
	var sum int64
	for _, t := range tables {
		var c int64
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{t}.Sanitize()).Scan(&c); err != nil {
			return 0, err
		}
		sum += c
	}
	return sum, nil
}

// pgLatency returns the latency percentiles of rows that landed after since,
// from the landed_at column the latency cells' trigger maintains.
func pgLatency(ctx context.Context, conn *pgx.Conn, tables []string, since time.Time) (map[string]int64, time.Time, error) {
	var parts []string
	for _, t := range tables {
		parts = append(parts, "SELECT landed_at, payload FROM "+pgx.Identifier{t}.Sanitize()+" WHERE landed_at > $1")
	}
	q := `SELECT count(*),
	  COALESCE(percentile_disc(0.5) WITHIN GROUP (ORDER BY lat),0),
	  COALESCE(percentile_disc(0.99) WITHIN GROUP (ORDER BY lat),0),
	  COALESCE(percentile_disc(0.999) WITHIN GROUP (ORDER BY lat),0),
	  COALESCE(max(landed_at), $1)
	FROM (SELECT landed_at, (extract(epoch from landed_at)*1000000)::bigint - substring(payload from 'T([0-9]{19})')::bigint/1000 AS lat
	      FROM (` + strings.Join(parts, " UNION ALL ") + `) u) l`
	var n, p50, p99, p999 int64
	var last time.Time
	if err := conn.QueryRow(ctx, q, since).Scan(&n, &p50, &p99, &p999, &last); err != nil {
		return nil, since, err
	}
	return map[string]int64{"lat_n": n, "lat_p50_us": p50, "lat_p99_us": p99, "lat_p999_us": p999}, last, nil
}

// ---- kafka

func kafkaTopics(ctx context.Context, adm *kadm.Client, pattern string) ([]string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	td, err := adm.ListTopics(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range td.Names() {
		if re.MatchString(t) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out, nil
}

// kafkaCount is the number of records in the matching topics: end minus start
// offset, per partition. Every pipeline writes one record per event and the
// topics are fresh per run, so this is the event count.
func kafkaCount(ctx context.Context, adm *kadm.Client, pattern string) (int64, error) {
	topics, err := kafkaTopics(ctx, adm, pattern)
	if err != nil || len(topics) == 0 {
		return 0, err
	}
	ends, err := adm.ListEndOffsets(ctx, topics...)
	if err != nil {
		return 0, err
	}
	starts, err := adm.ListStartOffsets(ctx, topics...)
	if err != nil {
		return 0, err
	}
	var sum int64
	ends.Each(func(o kadm.ListedOffset) {
		if s, ok := starts.Lookup(o.Topic, o.Partition); ok && o.Err == nil {
			sum += o.Offset - s.Offset
		}
	})
	return sum, nil
}

// kafkaTailLatency reads up to n records from the end of every partition and
// returns the latency from each record's marker to its broker append time.
// The brokers run with log.message.timestamp.type=LogAppendTime, so the record
// timestamp is when the broker accepted it, not what the producer claimed.
func kafkaTailLatency(ctx context.Context, e env, adm *kadm.Client, pattern string, n int64) (map[string]int64, error) {
	topics, err := kafkaTopics(ctx, adm, pattern)
	if err != nil || len(topics) == 0 {
		return percentiles(nil), err
	}
	ends, err := adm.ListEndOffsets(ctx, topics...)
	if err != nil {
		return nil, err
	}
	assign := map[string]map[int32]kgo.Offset{}
	want := int64(0)
	ends.Each(func(o kadm.ListedOffset) {
		from := o.Offset - n
		if from < 0 {
			from = 0
		}
		if o.Offset == from {
			return
		}
		if assign[o.Topic] == nil {
			assign[o.Topic] = map[int32]kgo.Offset{}
		}
		assign[o.Topic][o.Partition] = kgo.NewOffset().At(from)
		want += o.Offset - from
	})
	if want == 0 {
		return percentiles(nil), nil
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(e.brokers), kgo.ConsumePartitions(assign))
	if err != nil {
		return nil, err
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var lat []int64
	got := int64(0)
	for got < want && ctx.Err() == nil {
		fetches := cl.PollFetches(ctx)
		fetches.EachRecord(func(r *kgo.Record) {
			got++
			if ms := findMarkers(string(r.Value)); len(ms) > 0 {
				lat = append(lat, (r.Timestamp.UnixNano()-ms[len(ms)-1].TS)/1000)
			}
		})
	}
	return percentiles(lat), nil
}

// eachKafkaRecord reads every matching topic from its start offset to the end
// offset at call time, partition by partition order preserved.
func eachKafkaRecord(ctx context.Context, e env, pattern string, fn func(source, line string)) error {
	cl, err := kgo.NewClient(kgo.SeedBrokers(e.brokers))
	if err != nil {
		return err
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)
	topics, err := kafkaTopics(ctx, adm, pattern)
	if err != nil {
		return err
	}
	if len(topics) == 0 {
		return nil
	}
	ends, err := adm.ListEndOffsets(ctx, topics...)
	if err != nil {
		return err
	}
	starts, err := adm.ListStartOffsets(ctx, topics...)
	if err != nil {
		return err
	}
	for _, t := range topics {
		for p, end := range ends[t] {
			start := starts[t][p].Offset
			if end.Offset <= start {
				continue
			}
			pc, err := kgo.NewClient(kgo.SeedBrokers(e.brokers),
				kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{t: {p: kgo.NewOffset().At(start)}}),
				kgo.FetchMaxBytes(64<<20))
			if err != nil {
				return err
			}
			src := fmt.Sprintf("%s/%d", t, p)
			next := start
			for next < end.Offset {
				fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				fetches := pc.PollFetches(fctx)
				cancel()
				if fctx.Err() != nil && fetches.NumRecords() == 0 {
					pc.Close()
					return fmt.Errorf("%s: stalled at offset %d of %d", src, next, end.Offset)
				}
				fetches.EachRecord(func(r *kgo.Record) {
					if r.Offset < end.Offset {
						fn(src, string(r.Value))
					}
					next = r.Offset + 1
				})
			}
			pc.Close()
		}
	}
	return nil
}

// ---- s3 (MinIO)

func s3Client(e env) (*minio.Client, error) {
	host := strings.TrimPrefix(strings.TrimPrefix(e.s3URL, "http://"), "https://")
	return minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(e.s3Key, e.s3Sec, ""),
		Secure: strings.HasPrefix(e.s3URL, "https://"),
		Region: "us-east-1",
	})
}

type s3Counter struct {
	cl     *minio.Client
	bucket string
	seen   map[string]bool
	lines  int64
}

// count downloads only objects it has not seen. Both S3 sinks write immutable
// objects, so an object's line count never changes once listed. lat holds, for
// each new line with a marker, the object's LastModified minus the marker
// (one-second resolution: S3 timestamps are whole seconds).
func (c *s3Counter) count(ctx context.Context) (int64, []int64, error) {
	var lat []int64
	for obj := range c.cl.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			return c.lines, lat, obj.Err
		}
		if c.seen[obj.Key] {
			continue
		}
		r, err := c.cl.GetObject(ctx, c.bucket, obj.Key, minio.GetObjectOptions{})
		if err != nil {
			return c.lines, lat, err
		}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			c.lines++
			if ms := findMarkers(sc.Text()); len(ms) > 0 {
				lat = append(lat, (obj.LastModified.UnixNano()-ms[len(ms)-1].TS)/1000)
			}
		}
		r.Close()
		if err := sc.Err(); err != nil {
			return c.lines, lat, err
		}
		c.seen[obj.Key] = true
	}
	return c.lines, lat, nil
}

func eachS3Line(ctx context.Context, e env, bucket string, fn func(source, line string)) error {
	cl, err := s3Client(e)
	if err != nil {
		return err
	}
	var keys []string
	for obj := range cl.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			return obj.Err
		}
		keys = append(keys, obj.Key)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r, err := cl.GetObject(ctx, bucket, k, minio.GetObjectOptions{})
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			fn(k, sc.Text())
		}
		r.Close()
		if err := sc.Err(); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	return nil
}

// ---- http receiver

func httpStats(ctx context.Context, base string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/stats", nil)
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
