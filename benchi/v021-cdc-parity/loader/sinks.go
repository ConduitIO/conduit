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
//	pg:bench_dest_%d            source position applied: sum over the tables of
//	                            max(seq)+1 (seq is a generated, indexed column
//	                            decoded from the payload marker), so the count is
//	                            in source ops, like every other sink, and costs
//	                            one index probe per table instead of a scan
//	pgins:bench_dest_%d         inserts applied (pg_stat_user_tables), for the
//	                            snapshot cells, where every event is an insert
//	pgops:bench_dest_%d         inserts + updates + deletes applied
//	                            (pg_stat_user_tables): the @noseq control arms,
//	                            whose tables have no seq column
//	kafka:^cdcparitykafka\..*   records across the matching topics (end offsets)
//	s3:bench                    lines across every object in the bucket; NOT read
//	                            while a run is measured (see collect s3-index)
//	http:http://cdcparity-receiver:8090
//
// `sample` counts a sink repeatedly; `verify` reads it once, completely;
// `collect` indexes it after the run.
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
		case "file", "pg", "pgins", "pgops", "kafka", "s3", "http":
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

// pgCount is count(*) across the tables: verify and the end-of-run check only,
// never the sampler (it scans the tables the sink is writing).
func pgCount(ctx context.Context, conn *pgx.Conn, tables []string) (int64, error) {
	var sum int64
	for _, t := range tables {
		var c int64
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+pgIdent(t)).Scan(&c); err != nil {
			return 0, err
		}
		sum += c
	}
	return sum, nil
}

// pgPosition is the source position the destination has applied: per table
// the highest seq landed (+1, seq counts from 0), summed. One index probe per
// table (bench_dest_i_seq). Because seq is the per-table op index, deletes
// and updates in between count too: the result is comparable with the
// loader's committed ops and with the event sinks' counts.
func pgPosition(ctx context.Context, conn *pgx.Conn, tables []string) (int64, error) {
	var sum int64
	for _, t := range tables {
		var c int64
		if err := conn.QueryRow(ctx, "SELECT COALESCE(max(seq)+1, 0) FROM "+pgIdent(t)).Scan(&c); err != nil {
			return 0, err
		}
		sum += c
	}
	return sum, nil
}

// pgInserts is the inserts applied to the tables, from the statistics views.
func pgInserts(ctx context.Context, conn *pgx.Conn, tables []string) (int64, error) {
	var sum int64
	err := conn.QueryRow(ctx, "SELECT COALESCE(sum(n_tup_ins),0)::bigint FROM pg_stat_user_tables WHERE relname = ANY($1)", tables).Scan(&sum)
	return sum, err
}

// pgOps is the row writes applied to the tables (inserts, updates, deletes).
func pgOps(ctx context.Context, conn *pgx.Conn, tables []string) (int64, error) {
	var sum int64
	err := conn.QueryRow(ctx, "SELECT COALESCE(sum(n_tup_ins+n_tup_upd+n_tup_del),0)::bigint FROM pg_stat_user_tables WHERE relname = ANY($1)", tables).Scan(&sum)
	return sum, err
}

func pgIdent(t string) string {
	if s, n, ok := strings.Cut(t, "."); ok {
		return pgx.Identifier{s, n}.Sanitize()
	}
	return pgx.Identifier{t}.Sanitize()
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

// eachKafkaRecord reads every matching topic from its start offset to the end
// offset at call time, partition by partition order preserved. The line is
// key, a tab, then value, so a tombstone (no value) is still attributed by
// its key. With rec, the raw record is passed too (collect uses its
// timestamp).
func eachKafkaRecord(ctx context.Context, e env, pattern string, fn func(source, line string)) error {
	return eachKafkaRaw(ctx, e, pattern, func(src string, r *kgo.Record) { fn(src, string(r.Key)+"\t"+string(r.Value)) })
}

func eachKafkaRaw(ctx context.Context, e env, pattern string, fn func(source string, r *kgo.Record)) error {
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
						fn(src, r)
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
