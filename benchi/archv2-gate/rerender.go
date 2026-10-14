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
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
)

// Re-render mode re-reads finished measurements with the current reporting
// code. The runs are never touched: only how they are read can change, so a
// session measured with an older harness can be re-read with a newer rule.
//
// It takes either one matrix session directory (it has an env.json) or a
// matrix directory whose subdirectories are sessions, as matrix/run.sh lays
// them out. For a matrix directory it re-renders every complete session and
// writes matrix.md and matrix.json beside them: one table over all sessions.
//
// Reading is deliberately tolerant of sessions that did not finish, because a
// matrix takes hours and a laptop can sleep, a cell can fail, or the last cell
// can still be running:
//
//   - A session is "complete" only if env.json records a finish time and
//     runs.csv holds every planned run. Only complete sessions get a summary.md
//     written. matrix/run.sh treats a session with a summary.md as done, so
//     writing one for a partial session would make a resumed matrix skip it.
//   - An incomplete session is reported with what it has, marked incomplete.
//     Its comparisons use only rounds in which both arms ran both replicates.
//   - A session that cannot be read is reported as failed, with the reason,
//     and the other sessions are still rendered.
//   - A final runs.csv row that is short, or not newline-terminated, is a write
//     in progress (the harness rewrites the file after every run). It is
//     dropped and counted. A malformed row anywhere else is an error.

// cellStatus is how far a session got.
type cellStatus string

const (
	cellComplete   cellStatus = "complete"
	cellIncomplete cellStatus = "incomplete"
	cellFailed     cellStatus = "failed"
)

// cell is one matrix session as read back from disk.
type cell struct {
	Name   string
	Dir    string
	Status cellStatus
	// Note says why a session is incomplete or failed.
	Note     string
	Env      environment
	Results  []runResult
	Expected int
	// Dropped is the number of trailing runs.csv rows dropped as a write in
	// progress.
	Dropped     int
	Arms        []armReport
	Comparisons []comparison
}

func (c cell) config() config {
	cfg := config{
		Shape: c.Env.Shape, Session: sessionMatrix, Rounds: c.Env.Rounds, OutDir: c.Dir, Env: c.Env.BenchEnv,
		skipped: c.Env.SkippedArms,
	}
	cfg.Warmup, _ = time.ParseDuration(c.Env.Warmup)
	cfg.Window, _ = time.ParseDuration(c.Env.Window)
	for _, l := range c.Env.Arms {
		cfg.Arms = append(cfg.Arms, armSpec{Label: l})
	}
	return cfg
}

// loadCell reads one session directory. It never returns an error: a session
// that cannot be read comes back with Status cellFailed and the reason in Note.
func loadCell(dir string) cell {
	c := cell{Name: filepath.Base(dir), Dir: dir}
	fail := func(format string, args ...any) cell {
		c.Status, c.Note = cellFailed, fmt.Sprintf(format, args...)
		return c
	}

	rawEnv, err := os.ReadFile(filepath.Join(dir, "env.json")) //nolint:gosec // dir is the operator's own -rerender argument
	if err != nil {
		return fail("read env.json: %v", err)
	}
	if err := json.Unmarshal(rawEnv, &c.Env); err != nil {
		return fail("parse env.json: %v", err)
	}
	if c.Env.Session != string(sessionMatrix) {
		return fail("a %q session; only matrix sessions can be re-rendered", c.Env.Session)
	}
	if len(c.Env.Arms) == 0 {
		// env.json gets its arms after the pre-session probe, so a session
		// that ended in the probe has none.
		return fail("env.json lists no arms: the session ended before its probe finished")
	}
	c.Expected = 2 * len(c.Env.Arms) * c.Env.Rounds

	replicates := map[string]bool{}
	for _, l := range c.Env.Arms {
		replicates[l+"-a"], replicates[l+"-b"] = true, true
	}
	c.Results, c.Dropped, err = readRunsCSV(filepath.Join(dir, "runs.csv"), replicates)
	switch {
	case cerrors.Is(err, os.ErrNotExist):
		// The probe finished but no run did.
	case err != nil:
		return fail("%v", err)
	}

	cfg := c.config()
	for _, a := range cfg.Arms {
		c.Arms = append(c.Arms, reportArm(c.Results, a.Label))
	}
	for i := range c.Arms {
		for j := i + 1; j < len(c.Arms); j++ {
			c.Comparisons = append(c.Comparisons, compareArms(c.Results, c.Arms[i], c.Arms[j]))
		}
	}

	finished := !c.Env.Finished.IsZero()
	switch {
	case len(c.Results) > c.Expected:
		return fail("runs.csv has %d runs, more than the %d the plan has", len(c.Results), c.Expected)
	case finished && len(c.Results) < c.Expected:
		// The harness sets the finish time only after the last run is
		// written, so this is a damaged directory, not a partial session.
		return fail("env.json records a finish but runs.csv has %d of %d runs", len(c.Results), c.Expected)
	case !finished:
		c.Status = cellIncomplete
		c.Note = fmt.Sprintf("%d of %d runs, no finish recorded", len(c.Results), c.Expected)
	default:
		c.Status = cellComplete
	}
	if c.Dropped > 0 {
		c.Note = strings.TrimPrefix(c.Note+fmt.Sprintf("; %d partial trailing row dropped", c.Dropped), "; ")
	}
	return c
}

// rerender is the -rerender entry point. See the comment at the top of this
// file for what it reads and writes.
func rerender(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "env.json")); err == nil { //nolint:gosec // dir is the operator's own -rerender argument
		return rerenderSession(dir, os.Stdout)
	}
	return rerenderMatrix(dir, os.Stdout)
}

// rerenderSession re-renders one session. A complete session gets summary.md
// and summary.json rewritten. An incomplete one is rendered to w only, marked
// as such, and nothing in its directory changes.
func rerenderSession(dir string, w io.Writer) error {
	c := loadCell(dir)
	switch c.Status {
	case cellFailed:
		return cerrors.Errorf("%s: %s", dir, c.Note)
	case cellIncomplete:
		fmt.Fprintf(w, "INCOMPLETE SESSION (%s); summary.md not written\n\n", c.Note)
		_, err := io.WriteString(w, renderSummary(c.config(), c.Results))
		return err
	case cellComplete:
	}
	return writeCellSummary(c)
}

func writeCellSummary(c cell) error {
	cfg := c.config()
	out := renderSummary(cfg, c.Results)
	if err := writeMatrixJSON(cfg, c.Results); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.Dir, "summary.md"), []byte(out), 0o600) //nolint:gosec // dir is the operator's own -rerender argument
}

// rerenderMatrix re-renders every session under dir and writes matrix.md and
// matrix.json. A session that fails to read is reported, not fatal; the
// returned error only covers failing to write the outputs.
func rerenderMatrix(dir string, w io.Writer) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return cerrors.Errorf("read %s: %w", dir, err)
	}
	var cells []cell
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c := loadCell(filepath.Join(dir, e.Name()))
		if c.Status == cellComplete {
			if err := writeCellSummary(c); err != nil {
				return cerrors.Errorf("%s: %w", c.Name, err)
			}
		}
		fmt.Fprintf(w, "%-32s %-10s %s\n", c.Name, c.Status, c.Note)
		cells = append(cells, c)
	}
	if len(cells) == 0 {
		return cerrors.Errorf("%s has no env.json and no session subdirectories", dir)
	}
	// Run order, so the tables read in the order the machine went through
	// them. Sessions without a start time (failed reads) go last, by name.
	slices.SortStableFunc(cells, func(a, b cell) int {
		switch az, bz := a.Env.Started.IsZero(), b.Env.Started.IsZero(); {
		case az && bz:
			return 0
		case az:
			return 1
		case bz:
			return -1
		}
		return a.Env.Started.Compare(b.Env.Started)
	})
	if err := writeJSON(filepath.Join(dir, "matrix.json"), matrixIndexJSON(cells)); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "matrix.md"), []byte(renderMatrixIndex(filepath.Base(dir), cells)), 0o600) //nolint:gosec // dir is the operator's own -rerender argument
}

// readRunsCSV reads a session's runs.csv by column name. replicates, if not
// nil, is the set of arm names the session planned; a row naming any other
// arm is an error. It returns the number of trailing rows it dropped as a
// write in progress. An empty file is a session with no runs yet.
func readRunsCSV(path string, replicates map[string]bool) ([]runResult, int, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a runs.csv under the operator's own results directory
	if err != nil {
		return nil, 0, cerrors.Errorf("read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, 0, nil
	}
	// A file that does not end in a newline was cut off mid-write.
	truncated := raw[len(raw)-1] != '\n'

	r := csv.NewReader(bytes.NewReader(raw))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, 0, cerrors.Errorf("parse %s: %w", path, err)
	}
	header := rows[0]
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	for _, need := range []string{"round", "arm", "engine", "records_per_s_per_sink"} {
		if _, ok := col[need]; !ok {
			return nil, 0, cerrors.Errorf("%s: header has no %q column", path, need)
		}
	}

	rows = rows[1:]
	dropped := 0
	if n := len(rows); n > 0 && (truncated || len(rows[n-1]) != len(header)) {
		rows, dropped = rows[:n-1], 1
	}

	type key struct {
		round int
		arm   string
	}
	seen := map[key]bool{}
	var out []runResult
	for i, row := range rows {
		line := i + 2
		if len(row) != len(header) {
			return nil, 0, cerrors.Errorf("%s line %d: %d columns, header has %d", path, line, len(row), len(header))
		}
		round, err := strconv.Atoi(row[col["round"]])
		if err != nil || round < 1 {
			return nil, 0, cerrors.Errorf("%s line %d: round %q is not a positive integer", path, line, row[col["round"]])
		}
		rate, err := strconv.ParseFloat(row[col["records_per_s_per_sink"]], 64)
		if err != nil || rate < 0 {
			return nil, 0, cerrors.Errorf("%s line %d: rate %q is not a non-negative number", path, line, row[col["records_per_s_per_sink"]])
		}
		arm := row[col["arm"]]
		if replicates != nil && !replicates[arm] {
			return nil, 0, cerrors.Errorf("%s line %d: arm %q is not a replicate of any arm in env.json", path, line, arm)
		}
		k := key{round, arm}
		if seen[k] {
			return nil, 0, cerrors.Errorf("%s line %d: round %d arm %s appears twice", path, line, round, arm)
		}
		seen[k] = true
		out = append(out, runResult{runSpec: runSpec{Round: round, Arm: arm, Engine: engine(row[col["engine"]])}, Rate: rate})
	}
	return out, dropped, nil
}

// matrixIndex is matrix.json: every session's numbers in one file.
type matrixIndex struct {
	Sessions []matrixIndexCell `json:"sessions"`
}

type matrixIndexCell struct {
	Name        string     `json:"name"`
	Status      cellStatus `json:"status"`
	Note        string     `json:"note,omitempty"`
	Runs        int        `json:"runs"`
	Expected    int        `json:"expected_runs"`
	Summary     matrixJSON `json:"summary"`
	StartedUTC  time.Time  `json:"started,omitzero"`
	FinishedUTC time.Time  `json:"finished,omitzero"`
}

func matrixIndexJSON(cells []cell) matrixIndex {
	var mi matrixIndex
	for _, c := range cells {
		mc := matrixIndexCell{
			Name: c.Name, Status: c.Status, Note: c.Note, Runs: len(c.Results), Expected: c.Expected,
			StartedUTC: c.Env.Started, FinishedUTC: c.Env.Finished,
		}
		if c.Status != cellFailed {
			mc.Summary = buildMatrixJSON(c.config(), c.Arms, c.Comparisons)
		}
		mi.Sessions = append(mi.Sessions, mc)
	}
	return mi
}

// renderMatrixIndex renders matrix.md: one row per session, then one row per
// arm and per comparison, each tagged with its session. It states the reading
// rule in the file, because a table of medians read without it is the mistake
// this harness exists to prevent.
func renderMatrixIndex(name string, cells []cell) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# archv2-gate matrix: %s\n\n", name)
	b.WriteString("Generated by `archv2-gate -rerender` from each session's `runs.csv` and `env.json`; do not edit.\n" +
		"Rerun the command to regenerate it. Each session is one interleaved measurement: compare arms only\n" +
		"within a session, never a rate from one session with a rate from another.\n\n")

	b.WriteString("## Sessions\n\n| session | shape | bench knobs | status | runs | note |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, c := range cells {
		knobs := "none"
		if len(c.Env.BenchEnv) > 0 {
			knobs = "`" + strings.Join(c.Env.BenchEnv, " ") + "`"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d/%d | %s |\n",
			c.Name, orDash(c.Env.Shape), knobs, c.Status, len(c.Results), c.Expected, orDash(mdEscape(c.Note)))
	}
	b.WriteString("\n")

	b.WriteString("## Arms\n\n| session | arm | n | median rec/s | sd % | A/A floor (max) | A/A typical (median) |\n" +
		"| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, c := range cells {
		for _, a := range c.Arms {
			fmt.Fprintf(&b, "| %s | %s | %d | %.0f | %.1f | ±%.1f%% | ±%.1f%% |\n",
				c.Name, a.Label, a.Stats.N, a.Stats.Median, a.Stats.SDPct, a.FloorPct, a.TypicalAAPct)
		}
		for _, s := range c.Env.SkippedArms {
			fmt.Fprintf(&b, "| %s | %s | 0 | skipped: no output in the probe | | | |\n", c.Name, s.Label)
		}
	}
	b.WriteString("\n")

	b.WriteString("## Comparisons\n\n| session | comparison | median delta | per-round deltas | A/A floor | read |\n" +
		"| --- | --- | --- | --- | --- | --- |\n")
	for _, c := range cells {
		for _, cmp := range c.Comparisons {
			read := verdict(cmp)
			if c.Status != cellComplete {
				read += " (" + string(c.Status) + " session)"
			}
			fmt.Fprintf(&b, "| %s | %s vs %s | %+.1f%% | %s | ±%.1f%% | %s |\n",
				c.Name, cmp.Y, cmp.X, cmp.MedianDeltaPct, fmtPcts(cmp.RoundDeltas), cmp.FloorPct, read)
		}
	}
	b.WriteString("\n")
	b.WriteString("A comparison is _resolved_ only if its median delta exceeds the larger of the two arms' A/A floors\n" +
		"and every per-round delta has the same sign. _Suggestive_ means not resolved, but every per-round delta\n" +
		"has that sign and the median delta exceeds twice the larger typical A/A difference: a reason to rerun\n" +
		"on quiet hardware, not a result. Anything else is a tie as far as that session can tell.\n")
	return b.String()
}

func verdict(c comparison) string {
	switch {
	case c.Resolved:
		return "resolved"
	case c.Suggestive:
		return "suggestive"
	default:
		return "not resolved"
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// mdEscape keeps a free-text note inside one table cell.
func mdEscape(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.ReplaceAll(s, "|", `\|`)
}
