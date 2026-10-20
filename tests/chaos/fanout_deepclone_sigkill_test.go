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

package chaos

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/badger"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"
)

// Fan-out deep-clone case (ConduitIO/conduit#3151, conduit-commons#336):
// one source into three destinations, a custom.javascript processor on two of
// them editing the record's nested arrays, maps and bytes in place, none on
// the third, a SIGKILL while the branches are at different points, a restart
// on the same state.
//
// fanout_sigkill_test.go proves no record is lost between a fast and a slow
// branch across a kill, but its records carry no nested data and its branches
// no processors, so it cannot see a Record.Clone that shares nested data
// between branches. tests/valuecorrectness covers the clone, but with no
// crash. This case is the product: the same pipeline, killed mid-stream. It
// matters beyond the plain value check because a crash redelivers records
// through a different path than the first delivery (read from the persisted
// position, fanned out again, edited again), and the destinations are
// appended to across the two runs, so a corrupt value written before the kill
// stays in the durable output.
//
// The runtime is the real one (fanout_deepclone_child.go), on both engines.
// Each destination's file is judged record by record against the exact value
// that destination must hold; nothing is derived from the scripts' own logic.
const (
	// fanoutDeepTotal is the number of source records. The edit branch spins
	// fanoutDeepSpinMS per record (see fanoutDeepEdit), so the stream lasts at
	// least total*spin even on a fast machine: the kill cannot land after the
	// end of the run.
	fanoutDeepTotal  = 900
	fanoutDeepSpinMS = 5
	// fanoutDeepKillAfter is how many records destination A must hold before
	// the kill. At the spin rate that is about 2.5s of stream, longer than the
	// connector persister's 1s debounce (connector.DefaultPersisterDelayThreshold),
	// so a source position is persisted before the kill and the invariant 1
	// check below is not vacuous. The test asserts that it was.
	fanoutDeepKillAfter = 500
	// fanoutDeepLineLen is the length of one input line, "000042\n". The file
	// source's position is the byte offset after the line, so the record with
	// sequence number n carries position n*fanoutDeepLineLen.
	fanoutDeepLineLen = 7
)

// fanoutDeepBuild turns an input line ("000042") into the nested record every
// branch starts from. The array, map and bytes shapes are the ones a shallow
// Clone shares. Bytes are a Uint8Array, which the processor exports as a plain
// []byte inside the structured data.
const fanoutDeepBuild = `function process(rec) {
  var raw = rec.Payload.After;
  var line = "";
  for (var i = 0; i < raw.length; i++) { line += String.fromCharCode(raw[i]); }
  var sd = new StructuredData();
  sd["seq"] = parseInt(line, 10);
  sd["items"] = [{"sku": "A", "qty": 1, "tags": ["x", "y"]}, {"sku": "B", "qty": 2, "tags": ["z"]}];
  sd["nested"] = {"x": 1, "deeper": {"y": 2, "list": [1, 2, 3]}};
  sd["tags"] = ["a", "b"];
  sd["blob"] = new Uint8Array([1, 2, 3, 4]);
  rec.Payload.After = sd;
  return rec;
}`

// fanoutDeepEdit is the in-place edit of one mutating branch. It reaches
// into arrays (items, tags), arrays of maps (items[i]), a map (nested) and
// bytes (blob), but not through a map inside a map: a shallow Clone retypes
// nested map[string]any values, and a script that walks through one fails with
// a TypeError instead of showing the leak this case is about. mark and
// markByte differ per branch, so a branch that reads another branch's edit is
// visible and not only an unedited record. spinMS holds the branch back on
// every record: the other branches run ahead, which is the state a kill must
// catch (records durable in some destinations and not yet in the edit branch)
// and the window in which the edit overlaps the other branches' reads.
func fanoutDeepEdit(mark string, markByte, spinMS int) string {
	return fmt.Sprintf(`function process(rec) {
  var a = rec.Payload.After;
  a.items[0].qty = "%[1]s";
  a.items[0].tags[0] = "%[1]s";
  a.items[1].sku = "%[1]s";
  a.nested.x = "%[1]s";
  a.tags[0] = "%[1]s";
  a.blob[0] = %[2]d;
  var t = Date.now();
  while (Date.now() - t < %[3]d) {}
  return rec;
}`, mark, markByte, spinMS)
}

// fanoutDeepWant is the structured payload of the record with sequence number
// seq that a destination must hold. mark == "" is the destination without a
// processor: exactly what the pipeline processor built. Written out by hand,
// not derived by running the scripts' logic, so the expectation cannot share a
// bug with them.
func fanoutDeepWant(seq int, mark string, markByte byte) map[string]any {
	blob := []byte{1, 2, 3, 4}
	items0Qty, items0Tag0, items1SKU := any(float64(1)), any("x"), any("B")
	nestedX, tags0 := any(float64(1)), any("a")
	if mark != "" {
		blob[0] = markByte
		items0Qty, items0Tag0, items1SKU = mark, mark, mark
		nestedX, tags0 = mark, mark
	}
	return map[string]any{
		"seq": float64(seq),
		"items": []any{
			map[string]any{"sku": "A", "qty": items0Qty, "tags": []any{items0Tag0, "y"}},
			map[string]any{"sku": items1SKU, "qty": float64(2), "tags": []any{"z"}},
		},
		"nested": map[string]any{"x": nestedX, "deeper": map[string]any{"y": float64(2), "list": []any{float64(1), float64(2), float64(3)}}},
		"tags":   []any{tags0, "b"},
		"blob":   base64.StdEncoding.EncodeToString(blob),
	}
}

// fanoutDeepDest is one destination: its output file and the exact record
// value it must hold.
type fanoutDeepDest struct {
	name     string
	path     string
	mark     string
	markByte byte
}

func (d fanoutDeepDest) want(seq int) map[string]any {
	return fanoutDeepWant(seq, d.mark, d.markByte)
}

// TestSIGKILL_MidFanout_DeepClone runs the case on the classic engine and on
// arch-v2 (cfg.Preview.PipelineArchV2).
func TestSIGKILL_MidFanout_DeepClone(t *testing.T) {
	for _, engine := range []struct {
		name   string
		archV2 bool
	}{{"classic", false}, {"arch-v2", true}} {
		t.Run(engine.name, func(t *testing.T) {
			runFanoutDeepCase(t, engine.archV2)
		})
	}
}

func runFanoutDeepCase(t *testing.T, archV2 bool) {
	dir := t.TempDir()
	dests := writeFanoutDeepPipeline(t, dir)
	a, b, c := dests[0], dests[1], dests[2]

	// First run: SIGKILL once the edit branch has delivered fanoutDeepKillAfter
	// records, which is long before the stream ends.
	cp := spawnChildWithEnv(t, fanoutDeepChildEnv(dir, archV2))
	exited := make(chan struct{})
	go func() { _ = cp.reap(); close(exited) }()
	waitFanoutDeep(t, cp, exited, 90*time.Second, "destination A to hold enough records", func() bool {
		recs, _ := readFanoutDeepDest(t, a)
		return len(recs) >= fanoutDeepKillAfter
	})
	cp.sigkill(t)

	// Whatever each destination wrote before the kill is already durable and
	// is judged now, before the restart: a leak written then stays in the file.
	// A kill can leave a half-written last line (never acknowledged, so it is
	// redelivered); cut it so the restart appends after a whole line, which is
	// what a destination whose write is atomic would have left.
	pre := make(map[string]map[int]bool, len(dests))
	for _, d := range dests {
		if trimFanoutDeepTornTail(t, d.path) {
			t.Logf("destination %s: cut a half-written last line left by the kill", d.name)
		}
		recs, _ := readFanoutDeepDest(t, d)
		checkFanoutDeepRecords(t, "before restart", d, recs)
		pre[d.name] = seqSet(recs)
	}
	t.Logf("before restart: A holds %d records, B %d, C %d (of %d)", len(pre["A"]), len(pre["B"]), len(pre["C"]), fanoutDeepTotal)
	if len(pre["A"]) >= fanoutDeepTotal {
		t.Fatalf("the kill landed after destination A had every record: not a mid-stream kill")
	}

	// Invariant 1: a position is acknowledged upstream only after every
	// destination durably wrote the record. The persisted source position is
	// the last acknowledged one, so every record up to it must already be in
	// every destination's file. A shorter prefix in any destination would be a
	// record the source will never redeliver after the restart.
	persisted := persistedFanoutDeepPosition(t, dir)
	acked := int(persisted / fanoutDeepLineLen)
	t.Logf("persisted source position %d: %d records acknowledged", persisted, acked)
	if acked == 0 {
		t.Fatalf("no source position was persisted before the kill: the invariant 1 check would be vacuous (raise fanoutDeepKillAfter)")
	}
	if acked >= fanoutDeepTotal {
		t.Fatalf("every record was acknowledged before the kill: not a mid-stream kill")
	}
	for _, d := range []fanoutDeepDest{a, b, c} {
		for seq := 1; seq <= acked; seq++ {
			if !pre[d.name][seq] {
				t.Fatalf("invariant 1: record %d is acknowledged by the source (persisted position %d) but destination %s did not durably hold it before the kill", seq, persisted, d.name)
			}
		}
	}

	// Restart on the same state; stop it with SIGTERM once the parent sees every
	// record in every destination.
	cp2 := spawnChildWithEnv(t, fanoutDeepChildEnv(dir, archV2))
	exited2 := make(chan struct{})
	go func() { _ = cp2.reap(); close(exited2) }()
	waitFanoutDeep(t, cp2, exited2, 90*time.Second, "every destination to hold every record", func() bool {
		for _, d := range dests {
			recs, _ := readFanoutDeepDest(t, d)
			if len(seqSet(recs)) < fanoutDeepTotal {
				return false
			}
		}
		return true
	})
	cp2.sigterm(t, 30*time.Second) // invariant 7: the restarted run stops cleanly
	if _, ok := cp2.line(markerFanoutDeepDone); !ok {
		t.Fatalf("restarted run did not report a clean shutdown\n%s", cp2.diagnostics())
	}

	// Invariant 3 (at-least-once): after the restart every destination holds
	// every record, duplicates allowed, gaps not. And the value check again over
	// the whole file, both runs.
	for _, d := range dests {
		recs, torn := readFanoutDeepDest(t, d)
		if torn {
			t.Errorf("destination %s: half-written last line after a clean shutdown", d.name)
		}
		checkFanoutDeepRecords(t, "after restart", d, recs)
		seen := seqSet(recs)
		var missing []int
		for seq := 1; seq <= fanoutDeepTotal; seq++ {
			if !seen[seq] {
				missing = append(missing, seq)
			}
		}
		if len(missing) > 0 {
			t.Errorf("invariant 3: destination %s never received records %v (of 1..%d)", d.name, missing, fanoutDeepTotal)
		}
		t.Logf("destination %s: %d lines, %d distinct records (%d redelivered after the kill)", d.name, len(recs), len(seen), len(recs)-len(seen))
	}
}

// checkFanoutDeepRecords fails the test for every record of d that differs
// from the value d must hold, and for a record whose position is not the one
// its sequence number was read from.
func checkFanoutDeepRecords(t *testing.T, phase string, d fanoutDeepDest, recs []fanoutDeepRecord) {
	t.Helper()
	bad := 0
	for i, r := range recs {
		seq, ok := r.seq()
		if !ok {
			bad++
			if bad <= 3 {
				t.Errorf("%s: destination %s line %d has no sequence number: %v", phase, d.name, i, r.After)
			}
			continue
		}
		if want := strconv.Itoa(seq * fanoutDeepLineLen); r.Position != want {
			bad++
			if bad <= 3 {
				t.Errorf("%s: destination %s record %d has position %q, want %q", phase, d.name, seq, r.Position, want)
			}
		}
		if diff := cmp.Diff(d.want(seq), r.After); diff != "" {
			bad++
			if bad <= 3 {
				t.Errorf("%s: destination %s record %d differs from what it must hold (-want +got):\n%s", phase, d.name, seq, diff)
			}
		}
	}
	if bad > 0 {
		t.Errorf("%s: %d of %d records of destination %s are wrong", phase, bad, len(recs), d.name)
	}
}

// fanoutDeepRecord is one line of a file destination: the record as JSON.
type fanoutDeepRecord struct {
	Position string         // decimal, decoded from the record's base64 position
	After    map[string]any // payload.after
}

func (r fanoutDeepRecord) seq() (int, bool) {
	f, ok := r.After["seq"].(float64)
	return int(f), ok
}

// readFanoutDeepDest parses a destination's file. torn reports a last line
// that is not terminated by a newline (a write cut short by SIGKILL). A line
// that does not parse as a record fails the test, except the torn one.
func readFanoutDeepDest(t *testing.T, d fanoutDeepDest) (recs []fanoutDeepRecord, torn bool) {
	t.Helper()
	raw, err := os.ReadFile(d.path)
	if os.IsNotExist(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("read %s: %v", d.path, err)
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		torn = true
		raw = raw[:bytes.LastIndexByte(raw, '\n')+1]
	}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec struct {
			Position []byte `json:"position"`
			Payload  struct {
				After map[string]any `json:"after"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("destination %s: line is not a record: %v\n%s", d.name, err, line)
		}
		recs = append(recs, fanoutDeepRecord{Position: string(rec.Position), After: rec.Payload.After})
	}
	return recs, torn
}

// trimFanoutDeepTornTail cuts a file back to its last newline and reports
// whether it cut anything.
func trimFanoutDeepTornTail(t *testing.T, path string) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(raw) == 0 || raw[len(raw)-1] == '\n' {
		return false
	}
	if err := os.Truncate(path, int64(bytes.LastIndexByte(raw, '\n')+1)); err != nil {
		t.Fatalf("truncate %s: %v", path, err)
	}
	return true
}

func seqSet(recs []fanoutDeepRecord) map[int]bool {
	s := make(map[int]bool, len(recs))
	for _, r := range recs {
		if seq, ok := r.seq(); ok {
			s[seq] = true
		}
	}
	return s
}

// waitFanoutDeep polls cond until it holds. It fails the test early, with the
// child's output, if the child exits first: under a shallow Clone the engine
// can die on "concurrent map iteration and map write", and that should read as
// the failure it is and not as a timeout.
func waitFanoutDeep(t *testing.T, cp *childProcess, exited <-chan struct{}, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		select {
		case <-exited:
			t.Fatalf("child exited while waiting for %s\n%s", what, cp.diagnostics())
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s\n%s", timeout, what, cp.diagnostics())
}

// persistedFanoutDeepPosition reads the source connector's persisted position
// from the child's badger database. The child must be dead (badger takes an
// exclusive lock).
func persistedFanoutDeepPosition(t *testing.T, dir string) uint64 {
	t.Helper()
	db, err := badger.New(zerolog.Nop(), filepath.Join(dir, "conduit.db"))
	if err != nil {
		t.Fatalf("open the child's database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close the child's database: %v", err)
		}
	}()
	all, err := connector.NewStore(db, log.New(zerolog.Nop())).GetAll(context.Background())
	if err != nil {
		t.Fatalf("read connectors: %v", err)
	}
	for id, inst := range all {
		if inst.Type != connector.TypeSource {
			continue
		}
		state, ok := inst.State.(connector.SourceState)
		if !ok || len(state.Position) == 0 {
			return 0
		}
		pos, err := strconv.ParseUint(string(state.Position), 10, 64)
		if err != nil {
			t.Fatalf("source %s: position %q is not a file offset: %v", id, state.Position, err)
		}
		return pos
	}
	t.Fatalf("no source connector in the child's database")
	return 0
}

// writeFanoutDeepPipeline writes the input file and the pipeline YAML under
// dir and returns the three destinations: A and C edit their record in place,
// B has no processor.
func writeFanoutDeepPipeline(t *testing.T, dir string) []fanoutDeepDest {
	t.Helper()
	pipelinesDir := filepath.Join(dir, "pipelines")
	if err := os.MkdirAll(pipelinesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var in strings.Builder
	for i := 1; i <= fanoutDeepTotal; i++ {
		fmt.Fprintf(&in, "%06d\n", i)
	}
	inPath := filepath.Join(dir, "fanout-deep.in")
	if err := os.WriteFile(inPath, []byte(in.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	dests := []fanoutDeepDest{
		{name: "A", path: filepath.Join(dir, "a.out.jsonl"), mark: "A", markByte: 'A'},
		{name: "B", path: filepath.Join(dir, "b.out.jsonl")},
		{name: "C", path: filepath.Join(dir, "c.out.jsonl"), mark: "C", markByte: 'C'},
	}
	// Only A is held back (spin); C edits at full speed, so the two edit
	// branches also overlap each other.
	y := fmt.Sprintf(`version: 2.2
pipelines:
  - id: %[1]s
    status: running
    connectors:
      - id: src
        type: source
        plugin: builtin:file
        settings:
          path: %[2]s
      - id: dst-a
        type: destination
        plugin: builtin:file
        settings:
          path: %[3]s
        processors:
          - id: edit-a
            plugin: custom.javascript
            settings:
              script: |
%[6]s
      - id: dst-b
        type: destination
        plugin: builtin:file
        settings:
          path: %[4]s
      - id: dst-c
        type: destination
        plugin: builtin:file
        settings:
          path: %[5]s
        processors:
          - id: edit-c
            plugin: custom.javascript
            settings:
              script: |
%[7]s
    processors:
      - id: build
        plugin: custom.javascript
        settings:
          script: |
%[8]s
`, fanoutDeepPipelineID, inPath, dests[0].path, dests[1].path, dests[2].path,
		indentYAML(fanoutDeepEdit("A", 'A', fanoutDeepSpinMS), 16),
		indentYAML(fanoutDeepEdit("C", 'C', 0), 16),
		indentYAML(fanoutDeepBuild, 12))
	if err := os.WriteFile(filepath.Join(pipelinesDir, "fanout-deep.yaml"), []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	return dests
}

// indentYAML prefixes every line of s with n spaces, for embedding in a YAML
// block scalar.
func indentYAML(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}
