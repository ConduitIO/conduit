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

package valuecorrectness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/google/go-cmp/cmp"
	codec "github.com/iskorotkov/avro/v2"
	"github.com/rs/zerolog"
	"github.com/twmb/franz-go/pkg/sr"
)

// recordsPerPipeline is how many records each pipeline writes. More than one,
// so a pipeline whose first record is right but whose later ones are not
// (a cache filled concurrently) is still caught.
const recordsPerPipeline = 20

// encodeSchema builds a record schema that defines a record type R and an
// enum E inline (in nullable unions) and then uses both by name inside
// arrays. A reference by name is what the codec's process-wide encoder cache
// could not tell apart (conduit-commons#305).
func encodeSchema(name, rFields, eSymbols string) string {
	return `{"type":"record","name":"` + name + `","fields":[` +
		`{"name":"top_r","type":["null",{"type":"record","name":"R","fields":[` + rFields + `]}]},` +
		`{"name":"top_e","type":["null",{"type":"enum","name":"E","symbols":[` + eSymbols + `]}]},` +
		`{"name":"a","type":{"type":"array","items":"R"}},` +
		`{"name":"e","type":{"type":"array","items":"E"}}]}`
}

// decimalSchema defines a decimal fixed type fx at the given scale and uses
// it by name inside an array: the exact shape of the #305 report.
func decimalSchema(name string, scale int) string {
	return fmt.Sprintf(`{"type":"record","name":"%s","fields":[`+
		`{"name":"top","type":["null",{"type":"fixed","name":"fx","size":8,"logicalType":"decimal","precision":12,"scale":%d}]},`+
		`{"name":"a","type":{"type":"array","items":"fx"}}]}`, name, scale)
}

// valuePipeline is one pipeline under test and what it must write.
type valuePipeline struct {
	id      string
	subject string
	version int // the version the registry must assign; asserted at setup
	schema  string
	// kind is "encode" (structured record built by a JS processor, then
	// avro.encode) or "roundtrip" (wire bytes read from the source, decoded by
	// avro.decode and encoded again by avro.encode).
	kind string
	// want is the value every record must carry, in the codec's native form.
	want map[string]any
	// p, q and sym parameterise the JS processor for kind "encode".
	p, q int64
	sym  string

	schemaID int    // assigned by the registry
	in, out  string // file paths
}

func TestAvro_OverlappingNamedTypes_ManyPipelinesOneProcess(t *testing.T) {
	if os.Getenv("CONDUIT_VALUE_CORRECTNESS") != "1" {
		t.Skip("set CONDUIT_VALUE_CORRECTNESS=1 to run (see .github/workflows/value-correctness.yml)")
	}

	encWant := func(p, q int64, sym string) map[string]any {
		return map[string]any{
			"top_r": nil,
			"top_e": nil,
			"a":     []any{map[string]any{"p": p, "q": q}},
			"e":     []any{sym},
		}
	}
	decWant := map[string]any{
		"top": nil,
		"a":   []any{big.NewRat(3, 2), big.NewRat(1, 4), big.NewRat(-1234, 100)},
	}

	// Every pair below defines R, E or fx under the same full name with
	// different contents. Field order swaps, enum symbol order and decimal
	// scale each change the bytes for the same value, so an encoder built
	// for another pipeline's schema writes different bytes.
	pipelines := []*valuePipeline{
		{
			id: "enc-a", subject: "orders-a", version: 1, kind: "encode",
			schema: encodeSchema("one", `{"name":"p","type":"long"},{"name":"q","type":"long"}`, `"X","Y"`),
			p:      1, q: 2, sym: "X",
		},
		{
			id: "enc-b", subject: "orders-b", version: 1, kind: "encode",
			schema: encodeSchema("two", `{"name":"q","type":"long"},{"name":"p","type":"long"}`, `"Y","X"`),
			p:      3, q: 4, sym: "X",
		},
		{
			// A second version of the subject enc-a uses, as after a schema
			// change: same names, different definitions.
			id: "enc-a-v2", subject: "orders-a", version: 2, kind: "encode",
			schema: encodeSchema("one", `{"name":"q","type":"long"},{"name":"p","type":"long"}`, `"Z","X","Y"`),
			p:      5, q: 6, sym: "Y",
		},
		{
			id: "dec-6", subject: "amounts-6", version: 1, kind: "roundtrip",
			schema: decimalSchema("amounts", 6),
			want:   decWant,
		},
		{
			id: "dec-2", subject: "amounts-2", version: 1, kind: "roundtrip",
			schema: decimalSchema("amounts", 2),
			want:   decWant,
		},
	}
	for _, p := range pipelines {
		if p.kind == "encode" {
			p.want = encWant(p.p, p.q, p.sym)
		}
	}

	dir := t.TempDir()
	pipelinesDir := filepath.Join(dir, "pipelines")
	mustMkdir(t, pipelinesDir)

	logs := &lockedBuffer{}
	cfg := conduit.DefaultConfigWithBasePath(dir)
	cfg.DB.Type = conduit.DBTypeInMemory
	cfg.API.Enabled = false
	cfg.Pipelines.Path = pipelinesDir
	cfg.Pipelines.ExitOnDegraded = true
	cfg.Log.NewLogger = func(level, _ string) log.CtxLogger {
		l, _ := zerolog.ParseLevel(level)
		return log.New(zerolog.New(logs).With().Timestamp().Logger().Level(l))
	}

	r, err := conduit.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Register every schema before the runtime starts, so the IDs are known
	// when the roundtrip inputs are written.
	for _, p := range pipelines {
		ss, err := r.SchemaRegistry.CreateSchema(ctx, p.subject, sr.Schema{Type: sr.TypeAvro, Schema: p.schema})
		if err != nil {
			t.Fatalf("register %s: %v", p.subject, err)
		}
		if ss.Version != p.version {
			t.Fatalf("register %s: got version %d, want %d", p.subject, ss.Version, p.version)
		}
		p.schemaID = ss.ID
		p.in = filepath.Join(dir, p.id+".in")
		p.out = filepath.Join(dir, p.id+".out.jsonl")
		writeInput(t, p)
		writePipeline(t, pipelinesDir, p)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- r.Run(ctx) }()
	select {
	case <-r.Ready:
	case err := <-runErr:
		t.Fatalf("runtime exited before ready: %v\nlogs:\n%s", err, logs.String())
	case <-ctx.Done():
		t.Fatalf("runtime not ready: %v\nlogs:\n%s", ctx.Err(), logs.String())
	}
	if r.ProvisioningErr != nil {
		t.Fatalf("provisioning: %v\nlogs:\n%s", r.ProvisioningErr, logs.String())
	}

	// Start the pipelines one at a time and wait for each one's first record
	// before starting the next. The earlier pipeline's encoders are then
	// built before the later one encodes anything, so a shared cache is
	// always warm with another schema's types when it is consulted.
	for _, p := range pipelines {
		if err := r.Orchestrator.Pipelines.Start(ctx, p.id); err != nil {
			t.Fatalf("start %s: %v", p.id, err)
		}
		waitLines(ctx, t, p.out, 1, logs)
	}
	for _, p := range pipelines {
		waitLines(ctx, t, p.out, recordsPerPipeline, logs)
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(30 * time.Second):
		t.Error("runtime did not stop within 30s")
	}

	for _, p := range pipelines {
		t.Run(p.id, func(t *testing.T) {
			checkOutput(t, p)
		})
	}
}

// checkOutput reads every record p wrote and compares its bytes and its
// decoded value with what an isolated codec says they must be.
func checkOutput(t *testing.T, p *valuePipeline) {
	t.Helper()
	sch := parseIsolated(t, p.schema)
	wantBody, err := freshAPI().Marshal(sch, p.want)
	if err != nil {
		t.Fatalf("expected encoding: %v", err)
	}

	recs := readRecords(t, p.out)
	if len(recs) < recordsPerPipeline {
		t.Fatalf("got %d records, want at least %d", len(recs), recordsPerPipeline)
	}
	bad := 0
	for i, raw := range recs {
		if !checkRecord(t, p, sch, wantBody, i, raw) {
			bad++
		}
	}
	if bad > 0 {
		t.Logf("%d of %d records of pipeline %s (%s v%d) are wrong", bad, len(recs), p.id, p.subject, p.version)
	}
}

// freshAPI returns a codec config that has never encoded or decoded
// anything, so its caches only ever hold the one schema it is used with.
func freshAPI() codec.API { return codec.Config{}.Freeze() }

// checkRecord reports every way record i differs from what p must write and
// returns whether it is correct.
func checkRecord(t *testing.T, p *valuePipeline, sch codec.Schema, wantBody []byte, i int, raw []byte) bool {
	t.Helper()
	id, body, err := splitWire(raw)
	if err != nil {
		t.Errorf("record %d: %v", i, err)
		return false
	}
	ok := true
	if id != p.schemaID {
		t.Errorf("record %d: wire header names schema ID %d, want %d (%s v%d)", i, id, p.schemaID, p.subject, p.version)
		ok = false
	}
	if !bytes.Equal(body, wantBody) {
		t.Errorf("record %d: avro body\n got %x\nwant %x", i, body, wantBody)
		ok = false
	}
	var got map[string]any
	if err := freshAPI().Unmarshal(sch, body, &got); err != nil {
		t.Errorf("record %d: decode with %s v%d: %v", i, p.subject, p.version, err)
		return false
	}
	if diff := cmp.Diff(p.want, got, cmp.Comparer(func(a, b *big.Rat) bool { return a.Cmp(b) == 0 })); diff != "" {
		t.Errorf("record %d: decoded value differs from what was sent (-want +got):\n%s", i, diff)
		ok = false
	}
	return ok
}

// parseIsolated parses a schema with a cache of its own, so the expectation
// never depends on what else was parsed in this process.
func parseIsolated(t *testing.T, s string) codec.Schema {
	t.Helper()
	sch, err := codec.ParseWithCache(s, "", &codec.SchemaCache{})
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return sch
}

// writeInput writes the source file. For "encode" pipelines each line is just
// a record trigger. For "roundtrip" pipelines each line is the base64 of the
// Confluent wire bytes of the expected value under the pipeline's own schema
// ID, produced by an isolated codec.
func writeInput(t *testing.T, p *valuePipeline) {
	t.Helper()
	var b strings.Builder
	line := "x"
	if p.kind == "roundtrip" {
		body, err := freshAPI().Marshal(parseIsolated(t, p.schema), p.want)
		if err != nil {
			t.Fatalf("encode input for %s: %v", p.id, err)
		}
		line = base64.StdEncoding.EncodeToString(wire(p.schemaID, body))
	}
	for range recordsPerPipeline {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(p.in, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writePipeline(t *testing.T, dir string, p *valuePipeline) {
	t.Helper()
	var procs string
	switch p.kind {
	case "encode":
		procs = fmt.Sprintf(`
      - id: build
        plugin: custom.javascript
        settings:
          script: |
            function process(rec) {
              var sd = new StructuredData();
              sd["top_r"] = null;
              sd["top_e"] = null;
              sd["a"] = [{"p": %d, "q": %d}];
              sd["e"] = ["%s"];
              rec.Payload.After = sd;
              return rec;
            }`, p.p, p.q, p.sym)
	case "roundtrip":
		procs = `
      - id: unbase64
        plugin: base64.decode
        settings:
          field: .Payload.After
      - id: dec
        plugin: avro.decode`
	default:
		t.Fatalf("unknown kind %q", p.kind)
	}
	y := fmt.Sprintf(`version: 2.2
pipelines:
  - id: %[1]s
    status: stopped
    connectors:
      - id: src
        type: source
        plugin: builtin:file
        settings:
          path: %[2]s
      - id: dst
        type: destination
        plugin: builtin:file
        settings:
          path: %[3]s
    processors:%[4]s
      - id: enc
        plugin: avro.encode
        settings:
          schema.strategy: preRegistered
          schema.preRegistered.subject: %[5]s
          schema.preRegistered.version: %[6]d
`, p.id, p.in, p.out, procs, p.subject, p.version)
	if err := os.WriteFile(filepath.Join(dir, p.id+".yaml"), []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
}

// wire prepends the Confluent wire header (magic byte 0, big-endian ID).
func wire(id int, body []byte) []byte {
	out := make([]byte, 5, 5+len(body))
	binary.BigEndian.PutUint32(out[1:], uint32(id))
	return append(out, body...)
}

func splitWire(b []byte) (int, []byte, error) {
	if len(b) < 5 || b[0] != 0 {
		return 0, nil, fmt.Errorf("not Confluent wire format: %x", b)
	}
	return int(binary.BigEndian.Uint32(b[1:5])), b[5:], nil
}

// readRecords returns the raw .Payload.After of every record in a file
// destination's output.
func readRecords(t *testing.T, path string) [][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var rec struct {
			Payload struct {
				After []byte `json:"after"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("%s: %v: %s", path, err, sc.Text())
		}
		out = append(out, rec.Payload.After)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func waitLines(ctx context.Context, t *testing.T, path string, n int, logs *lockedBuffer) {
	t.Helper()
	for {
		if b, err := os.ReadFile(path); err == nil && bytes.Count(b, []byte{'\n'}) >= n {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: fewer than %d records before deadline: %v\nlogs:\n%s", path, n, ctx.Err(), logs.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
