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

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

const runsHeader = "round,arm,engine,started_utc,window_s,sink_records,sink_bytes,records_per_s_per_sink,host_load,vm_load_before,vm_load_after\n"

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadRunsCSV(t *testing.T) {
	row := func(round, arm, rate string) string {
		return round + "," + arm + ",v1,2026-10-14T00:00:00Z,25.0,1,1," + rate + ",1 1 1,1 1 1,1 1 1\n"
	}
	reps := map[string]bool{"x-a": true, "x-b": true}

	t.Run("round trip with writeRunsCSV", func(t *testing.T) {
		is := is.New(t)
		p := filepath.Join(t.TempDir(), "runs.csv")
		in := []runResult{rr(1, "x-a", 100.5), rr(1, "x-b", 99)}
		is.NoErr(writeRunsCSV(p, in))
		got, dropped, err := readRunsCSV(p, reps)
		is.NoErr(err)
		is.Equal(dropped, 0)
		is.Equal(len(got), 2)
		is.Equal(got[0].Arm, "x-a")
		is.Equal(got[0].Rate, 100.5)
	})

	t.Run("columns are read by name", func(t *testing.T) {
		is := is.New(t)
		p := filepath.Join(t.TempDir(), "runs.csv")
		writeFile(t, p, "records_per_s_per_sink,arm,round,engine\n42,x-b,3,v2\n")
		got, _, err := readRunsCSV(p, nil)
		is.NoErr(err)
		is.Equal(got[0].Round, 3)
		is.Equal(got[0].Arm, "x-b")
		is.Equal(got[0].Rate, 42.0)
		is.Equal(got[0].Engine, engineV2)
	})

	t.Run("a write in progress drops only the last row", func(t *testing.T) {
		for name, tail := range map[string]string{
			"no trailing newline": "1,x-b,v1,2026-10-14T00:00:00Z,25.0,1,1,99.0,1 1 1,1 1 1,1 1 1",
			"short row":           "1,x-b,v1,2026-10-14T00:00:00Z,25.0,1,1,99\n",
			"cut mid-field":       "1,x-b,v1,2026-10-14T00:",
		} {
			t.Run(name, func(t *testing.T) {
				is := is.New(t)
				p := filepath.Join(t.TempDir(), "runs.csv")
				writeFile(t, p, runsHeader+row("1", "x-a", "100.0")+tail)
				got, dropped, err := readRunsCSV(p, reps)
				is.NoErr(err)
				is.Equal(dropped, 1)
				is.Equal(len(got), 1)
				is.Equal(got[0].Arm, "x-a")
			})
		}
	})

	t.Run("empty file is no runs", func(t *testing.T) {
		is := is.New(t)
		p := filepath.Join(t.TempDir(), "runs.csv")
		writeFile(t, p, "")
		got, dropped, err := readRunsCSV(p, reps)
		is.NoErr(err)
		is.Equal(len(got)+dropped, 0)
	})

	for name, content := range map[string]string{
		"short row not at the end": runsHeader + "1,x-a,v1\n" + row("1", "x-b", "1"),
		"duplicate run":            runsHeader + row("1", "x-a", "1") + row("1", "x-a", "2"),
		"arm not in env.json":      runsHeader + row("1", "y-a", "1"),
		"bad rate":                 runsHeader + row("1", "x-a", "fast") + row("1", "x-b", "1"),
		"round zero":               runsHeader + row("0", "x-a", "1") + row("1", "x-b", "1"),
		"missing column":           "round,arm,engine\n1,x-a,v1\n",
	} {
		t.Run("error: "+name, func(t *testing.T) {
			is := is.New(t)
			p := filepath.Join(t.TempDir(), "runs.csv")
			writeFile(t, p, content)
			_, _, err := readRunsCSV(p, reps)
			is.True(err != nil)
		})
	}
}

// writeSession writes a two-arm, two-round matrix session: x at 100 and y at
// 150, every run planned. runs limits how many runs are written; finished sets
// the finish time.
func writeSession(t *testing.T, dir string, runs int, finished bool) {
	t.Helper()
	writeSessionAt(t, dir, runs, finished, time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))
}

func writeSessionAt(t *testing.T, dir string, runs int, finished bool, started time.Time) {
	t.Helper()
	env := environment{
		Started: started, Shape: "1x1", Session: "matrix",
		Rounds: 2, Warmup: "8s", Window: "25s", Arms: []string{"x", "y"},
	}
	if finished {
		env.Finished = env.Started.Add(time.Hour)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "env.json"), env); err != nil {
		t.Fatal(err)
	}
	var res []runResult
	for _, s := range planMatrix([]armSpec{{Label: "x"}, {Label: "y"}}, 2) {
		rate := 100.0
		if strings.HasPrefix(s.Arm, "y") {
			rate = 150
		}
		res = append(res, runResult{runSpec: s, Rate: rate + float64(s.Round)})
	}
	if err := writeRunsCSV(filepath.Join(dir, "runs.csv"), res[:runs]); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCell(t *testing.T) {
	is := is.New(t)
	root := t.TempDir()

	writeSession(t, filepath.Join(root, "done"), 8, true)
	c := loadCell(filepath.Join(root, "done"))
	is.Equal(c.Status, cellComplete)
	is.Equal(c.Expected, 8)
	is.Equal(len(c.Comparisons), 1)
	is.True(c.Comparisons[0].Resolved) // y is 50% above x in both rounds, floors near 0

	writeSession(t, filepath.Join(root, "running"), 5, false)
	c = loadCell(filepath.Join(root, "running"))
	is.Equal(c.Status, cellIncomplete)
	is.True(strings.Contains(c.Note, "5 of 8"))
	// Round 2 lacks y-b, so only round 1 is compared, and one round is not
	// enough to resolve anything.
	is.Equal(len(c.Comparisons[0].RoundDeltas), 1)
	is.True(!c.Comparisons[0].Resolved)

	writeSession(t, filepath.Join(root, "damaged"), 5, true)
	is.Equal(loadCell(filepath.Join(root, "damaged")).Status, cellFailed)

	is.Equal(loadCell(filepath.Join(root, "missing")).Status, cellFailed)

	// Ended in the probe: env.json without arms and no runs.csv.
	is.NoErr(os.MkdirAll(filepath.Join(root, "probe"), 0o755))
	is.NoErr(writeJSON(filepath.Join(root, "probe", "env.json"), environment{Session: "matrix", Rounds: 2}))
	c = loadCell(filepath.Join(root, "probe"))
	is.Equal(c.Status, cellFailed)
	is.True(strings.Contains(c.Note, "probe"))

	// Probe finished, no run did yet.
	writeSession(t, filepath.Join(root, "fresh"), 0, false)
	is.NoErr(os.Remove(filepath.Join(root, "fresh", "runs.csv")))
	is.Equal(loadCell(filepath.Join(root, "fresh")).Status, cellIncomplete)
}

func TestRerenderMatrix(t *testing.T) {
	is := is.New(t)
	root := t.TempDir()
	t0 := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	// Name order is a, b, c, d; run order is d, a, b, and c failed to read.
	writeSessionAt(t, filepath.Join(root, "a-done"), 8, true, t0.Add(time.Hour))
	writeSessionAt(t, filepath.Join(root, "b-running"), 5, false, t0.Add(2*time.Hour))
	writeFile(t, filepath.Join(root, "c-broken", "env.json"), "{not json")
	writeSessionAt(t, filepath.Join(root, "d-first"), 8, true, t0)
	writeFile(t, filepath.Join(root, "README.md"), "hand-written")

	var out bytes.Buffer
	is.NoErr(rerenderMatrix(root, &out))

	// Only the complete session gets a summary: matrix/run.sh treats a
	// summary.md as "done" and would skip a partial session on resume.
	_, err := os.Stat(filepath.Join(root, "a-done", "summary.md"))
	is.NoErr(err)
	_, err = os.Stat(filepath.Join(root, "a-done", "summary.json"))
	is.NoErr(err)
	_, err = os.Stat(filepath.Join(root, "b-running", "summary.md"))
	is.True(os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(root, "b-running", "summary.json"))
	is.True(os.IsNotExist(err))

	md, err := os.ReadFile(filepath.Join(root, "matrix.md"))
	is.NoErr(err)
	for _, want := range []string{
		"| a-done | 1x1 | none | complete | 8/8 |",
		"| b-running | 1x1 | none | incomplete | 5/8 |",
		"| c-broken | - | none | failed | 0/0 |",
		"| a-done | y vs x | +",
		"| resolved |",
		"not resolved (incomplete session)",
	} {
		is.True(strings.Contains(string(md), want)) // matrix.md row missing
	}
	readme, _ := os.ReadFile(filepath.Join(root, "README.md"))
	is.Equal(string(readme), "hand-written") // prose next to the matrix is not touched

	var mi matrixIndex
	raw, err := os.ReadFile(filepath.Join(root, "matrix.json"))
	is.NoErr(err)
	is.NoErr(json.Unmarshal(raw, &mi))
	is.Equal(len(mi.Sessions), 4)
	var order []string
	for _, s := range mi.Sessions {
		order = append(order, s.Name)
	}
	is.Equal(order, []string{"d-first", "a-done", "b-running", "c-broken"}) // run order, failed last
	is.Equal(mi.Sessions[1].Status, cellComplete)
	is.Equal(len(mi.Sessions[1].Summary.Comparisons), 1)
	is.Equal(mi.Sessions[3].Status, cellFailed)

	// Rendering is idempotent: a second pass over the same runs writes the
	// same files.
	is.NoErr(rerenderMatrix(root, &bytes.Buffer{}))
	md2, _ := os.ReadFile(filepath.Join(root, "matrix.md"))
	is.Equal(string(md), string(md2))
}

func TestSkippedArmReasonIsFenced(t *testing.T) {
	is := is.New(t)
	cfg := config{
		Shape: "1x1", Session: sessionMatrix, Rounds: 2, Arms: []armSpec{{Label: "x"}},
		skipped: []skippedArm{{Label: "v2", Reason: "ERR can't start pipeline on conduit " + strings.Repeat("x ", 100) + "```"}},
	}
	out := renderSummary(cfg, nil)
	is.True(strings.Contains(out, "- v2:\n\n  ```text\n  ERR can't start pipeline on conduit ")) // reason in a fenced block
	is.Equal(strings.Count(out, "```"), 2)                                                       // a fence in the reason cannot close the block
}

func TestRerenderSessionIncompleteWritesNothing(t *testing.T) {
	is := is.New(t)
	dir := filepath.Join(t.TempDir(), "s")
	writeSession(t, dir, 3, false)
	before, _ := os.ReadDir(dir)

	var out bytes.Buffer
	is.NoErr(rerenderSession(dir, &out))
	is.True(strings.HasPrefix(out.String(), "INCOMPLETE SESSION"))
	after, _ := os.ReadDir(dir)
	is.Equal(len(before), len(after))

	writeSession(t, dir, 8, true)
	is.NoErr(rerenderSession(dir, &out))
	_, err := os.Stat(filepath.Join(dir, "summary.md"))
	is.NoErr(err)
}
