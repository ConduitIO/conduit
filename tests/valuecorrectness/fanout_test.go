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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"
)

// fanoutRecords is how many records the fan-out pipeline moves. More than one,
// so a leak that only some records show (a race between branches) is caught.
const fanoutRecords = 50

// The record the source side builds, as JSON. Nested arrays, maps below maps
// and arrays of maps: the shapes a shallow opencdc.Record.Clone shares
// (conduit#3151, conduit-commons#336).
const fanoutWantAfter = `{
  "items":  [{"sku": "A", "qty": 1, "tags": ["x", "y"]}, {"sku": "B", "qty": 2, "tags": ["z"]}],
  "nested": {"x": 1, "deeper": {"y": 2, "list": [1, 2, 3]}},
  "tags":   ["a", "b"]
}`

// fanoutEdit is the in-place edit a destination-scoped custom.javascript
// processor applies to the record it was handed. mark differs per branch, so
// a branch that reads another branch's mark is visible, not only an unedited
// record.
func fanoutEdit(mark string) string {
	return fmt.Sprintf(`function process(rec) {
  var a = rec.Payload.After;
  a.items[0].qty = "%[1]s";
  a.items[0].tags[0] = "%[1]s";
  a.items[1].sku = "%[1]s";
  a.tags[0] = "%[1]s";
  return rec;
}`, mark)
}

// TestFanout_NestedEditInOneDestinationDoesNotReachSiblings runs one source
// into three destinations, on each engine. Destinations A and C each have a
// custom.javascript processor that edits the record's nested arrays and maps
// in place, with different values; destination B has no processor. Every
// destination file is compared with what that destination must contain: B
// exactly the record the source produced, A and C exactly their own edit.
//
// This is the end-to-end form of the engine regression tests for #3151: fan-out
// gives each branch its own copy via opencdc.Record.Clone (classic:
// pkg/lifecycle/stream.FanoutNode; arch-v2: pkg/lifecycle-poc/funnel
// Batch.clone). With a shallow Clone the branches share nested data, B reads
// A's or C's edit, and A and C overwrite each other's.
func TestFanout_NestedEditInOneDestinationDoesNotReachSiblings(t *testing.T) {
	if os.Getenv("CONDUIT_VALUE_CORRECTNESS") != "1" {
		t.Skip("set CONDUIT_VALUE_CORRECTNESS=1 to run (see .github/workflows/value-correctness.yml)")
	}
	for _, engine := range []struct {
		name   string
		archV2 bool
	}{{"classic", false}, {"arch-v2", true}} {
		t.Run(engine.name, func(t *testing.T) {
			runFanout(t, engine.archV2)
		})
	}
}

func runFanout(t *testing.T, archV2 bool) {
	dir := t.TempDir()
	pipelinesDir := filepath.Join(dir, "pipelines")
	mustMkdir(t, pipelinesDir)

	in := filepath.Join(dir, "fanout.in")
	if err := os.WriteFile(in, []byte(strings.Repeat("x\n", fanoutRecords)), 0o600); err != nil {
		t.Fatal(err)
	}
	outs := map[string]string{
		"a": filepath.Join(dir, "a.out.jsonl"),
		"b": filepath.Join(dir, "b.out.jsonl"),
		"c": filepath.Join(dir, "c.out.jsonl"),
	}

	build := `function process(rec) {
  var sd = new StructuredData();
  sd["items"] = [{"sku": "A", "qty": 1, "tags": ["x", "y"]}, {"sku": "B", "qty": 2, "tags": ["z"]}];
  sd["nested"] = {"x": 1, "deeper": {"y": 2, "list": [1, 2, 3]}};
  sd["tags"] = ["a", "b"];
  rec.Payload.After = sd;
  return rec;
}`
	y := fmt.Sprintf(`version: 2.2
pipelines:
  - id: fanout
    status: stopped
    connectors:
      - id: src
        type: source
        plugin: builtin:file
        settings:
          path: %[1]s
      - id: dst-a
        type: destination
        plugin: builtin:file
        settings:
          path: %[2]s
        processors:
          - id: edit-a
            plugin: custom.javascript
            settings:
              script: |
%[5]s
      - id: dst-b
        type: destination
        plugin: builtin:file
        settings:
          path: %[3]s
      - id: dst-c
        type: destination
        plugin: builtin:file
        settings:
          path: %[4]s
        processors:
          - id: edit-c
            plugin: custom.javascript
            settings:
              script: |
%[6]s
    processors:
      - id: build
        plugin: custom.javascript
        settings:
          script: |
%[7]s
`, in, outs["a"], outs["b"], outs["c"],
		indent(fanoutEdit("A"), 16), indent(fanoutEdit("C"), 16), indent(build, 12))
	if err := os.WriteFile(filepath.Join(pipelinesDir, "fanout.yaml"), []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}

	logs := &lockedBuffer{}
	cfg := conduit.DefaultConfigWithBasePath(dir)
	// The pipelines read and write files under dir; the file connector is
	// confined to operator-allowed directories (GHSA-m3mw-fpw8-w765).
	cfg.Connectors.File.AllowedDirs = []string{dir}
	cfg.DB.Type = conduit.DBTypeInMemory
	cfg.API.Enabled = false
	cfg.Pipelines.Path = pipelinesDir
	cfg.Pipelines.ExitOnDegraded = true
	cfg.Preview.PipelineArchV2 = archV2
	cfg.Log.NewLogger = func(level, _ string) log.CtxLogger {
		l, _ := zerolog.ParseLevel(level)
		return log.New(zerolog.New(logs).With().Timestamp().Logger().Level(l))
	}

	r, err := conduit.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	runErr := make(chan error, 1)
	runDone := make(chan struct{})
	go func() {
		runErr <- r.Run(ctx)
		close(runDone)
	}()
	// Registered as a cleanup so the runtime is stopped on every path, before
	// t.TempDir removes dir (same reason as in the Avro test).
	stopRuntime := func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(30 * time.Second):
			t.Error("runtime did not stop within 30s")
		}
	}
	t.Cleanup(stopRuntime)

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
	if err := r.Orchestrator.Pipelines.Start(ctx, "fanout"); err != nil {
		t.Fatalf("start: %v", err)
	}
	for _, p := range outs {
		waitLines(ctx, t, p, fanoutRecords, logs)
	}
	stopRuntime()

	var base map[string]any
	if err := json.Unmarshal([]byte(fanoutWantAfter), &base); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]any{
		"a": editedWant("A"),
		"b": base, // no processor: exactly what the source side built
		"c": editedWant("C"),
	}
	for name, path := range outs {
		t.Run("dst-"+name, func(t *testing.T) {
			got := readAfterObjects(t, path)
			if len(got) < fanoutRecords {
				t.Fatalf("got %d records, want at least %d", len(got), fanoutRecords)
			}
			bad := 0
			for i, g := range got {
				if diff := cmp.Diff(want[name], g); diff != "" {
					bad++
					if bad <= 3 {
						t.Errorf("record %d of destination %s differs from what it must carry (-want +got):\n%s", i, name, diff)
					}
				}
			}
			if bad > 0 {
				t.Logf("%d of %d records of destination %s are wrong", bad, len(got), name)
			}
		})
	}
}

// editedWant is fanoutWantAfter after fanoutEdit(mark), written out by hand
// rather than derived by running the same logic, so the expectation does not
// share a bug with the script.
func editedWant(mark string) map[string]any {
	return map[string]any{
		"items": []any{
			map[string]any{"sku": "A", "qty": mark, "tags": []any{mark, "y"}},
			map[string]any{"sku": mark, "qty": float64(2), "tags": []any{"z"}},
		},
		"nested": map[string]any{"x": float64(1), "deeper": map[string]any{"y": float64(2), "list": []any{float64(1), float64(2), float64(3)}}},
		"tags":   []any{mark, "b"},
	}
}

// readAfterObjects returns the structured .Payload.After of every record in a
// file destination's output.
func readAfterObjects(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var rec struct {
			Payload struct {
				After map[string]any `json:"after"`
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

// indent prefixes every line of s with n spaces, for embedding in YAML.
func indent(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}
