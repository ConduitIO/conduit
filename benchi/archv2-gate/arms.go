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
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// sessionMatrix runs any number of arms in one session. Every arm runs twice
// per round, so every arm carries its own A/A control (see planMatrix).
const sessionMatrix session = "matrix"

// armSpec is one arm of a matrix session: a label, the pipeline engine, and
// optionally a Conduit binary to run instead of the image's own.
type armSpec struct {
	Label  string
	Engine engine
	// Binary is an absolute path to a linux Conduit binary, mounted into the
	// base image. Empty means the binary baked into -image.
	Binary string
	// Shape overrides the session's -shape for this arm, so one interleaved
	// session can compare configurations (for example source batching on and
	// off) as well as engines. Empty means the session's shape.
	Shape string
}

var armLabelRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.]*(-[a-z0-9.]+)*$`)

// parseArm parses "label=engine[:/abs/path/to/conduit][@shape]".
//
// A label may not end in "-a" or "-b": those suffixes name the two replicates
// of an arm and a label that ended in one would make the replicate names
// ambiguous.
func parseArm(s string) (armSpec, error) {
	label, rest, ok := strings.Cut(s, "=")
	if !ok || label == "" || rest == "" {
		return armSpec{}, cerrors.Errorf("-arm %q: want label=engine or label=engine:/path/to/conduit", s)
	}
	if !armLabelRe.MatchString(label) {
		return armSpec{}, cerrors.Errorf("-arm %q: label must be lower-case letters, digits, '.' and '-'", s)
	}
	if strings.HasSuffix(label, "-a") || strings.HasSuffix(label, "-b") {
		return armSpec{}, cerrors.Errorf("-arm %q: label must not end in -a or -b (those name the A/A replicates)", s)
	}
	var shape string
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest, shape = rest[:i], rest[i+1:]
		if shape == "" || strings.ContainsAny(shape, "/\\") {
			return armSpec{}, cerrors.Errorf("-arm %q: @shape must name a directory under -shapes-dir", s)
		}
	}
	eng, bin, _ := strings.Cut(rest, ":")
	a := armSpec{Label: label, Engine: engine(eng), Binary: bin, Shape: shape}
	if a.Engine != engineV1 && a.Engine != engineV2 {
		return armSpec{}, cerrors.Errorf("-arm %q: engine must be v1 or v2, got %q", s, eng)
	}
	if bin != "" {
		if !filepath.IsAbs(bin) {
			return armSpec{}, cerrors.Errorf("-arm %q: binary path must be absolute", s)
		}
		if _, err := os.Stat(bin); err != nil {
			return armSpec{}, cerrors.Errorf("-arm %q: %w", s, err)
		}
	}
	return a, nil
}

// benchEnvPrefix is the namespace of the bench knobs a session may inject. The
// Go runtime knobs below are the only other variables allowed. The harness
// records every injected pair in env.json, so the list is closed: nothing from
// the harness's own environment is ever passed through.
const benchEnvPrefix = "CONDUIT_BENCH_"

var runtimeEnv = []string{"GOMAXPROCS", "GOGC", "GODEBUG"}

func validateEnv(kv string) error {
	k, _, ok := strings.Cut(kv, "=")
	if !ok || !(strings.HasPrefix(k, benchEnvPrefix) || slices.Contains(runtimeEnv, k)) {
		return cerrors.Errorf("-env %q: want %s<NAME>=<value> or one of %s", kv, benchEnvPrefix, strings.Join(runtimeEnv, ", "))
	}
	return nil
}

// planMatrix returns the run order of a matrix session.
//
// Each round runs every arm twice in a palindrome (a b c c b a), with the
// leading arm rotated every round. The first run of an arm is its "-a"
// replicate and the second its "-b". The two replicates of one arm are that
// arm's A/A pair, so every arm has its own noise floor, taken in the same
// session and under the same conditions as the comparison it is read beside.
// The palindrome cancels linear drift inside a round, and the rotation spreads
// any warm-up or load trend over every arm's first and last positions.
func planMatrix(arms []armSpec, rounds int) []runSpec {
	var out []runSpec
	n := len(arms)
	for r := 1; r <= rounds; r++ {
		order := make([]armSpec, 0, n)
		for i := range arms {
			order = append(order, arms[(i+r-1)%n])
		}
		for _, a := range order {
			out = append(out, runSpec{Round: r, Arm: a.Label + "-a", Engine: a.Engine, Binary: a.Binary, Shape: a.Shape})
		}
		for _, a := range slices.Backward(order) {
			out = append(out, runSpec{Round: r, Arm: a.Label + "-b", Engine: a.Engine, Binary: a.Binary, Shape: a.Shape})
		}
	}
	return out
}

// armReport summarizes one arm of a matrix session.
type armReport struct {
	Label string
	// Stats is over all runs of the arm, both replicates.
	Stats armStats
	// FloorPct is the largest per-round |delta| between the arm's two
	// replicates: the noise this arm showed against itself in this session.
	FloorPct float64
	// AADeltas are the per-round replicate deltas the floor comes from.
	AADeltas []float64
	// TypicalAAPct is the median per-round |replicate delta|: what a normal
	// round looked like, as opposed to FloorPct, which one disturbed run sets.
	TypicalAAPct float64
}

// comparison is the result of reading arm Y against arm X.
type comparison struct {
	X, Y string
	// MedianDeltaPct is (median(Y) - median(X)) / median(X) over all runs.
	MedianDeltaPct float64
	// RoundDeltas are per-round deltas of the replicate means.
	RoundDeltas []float64
	// FloorPct is the larger of the two arms' A/A floors.
	FloorPct float64
	// Resolved is true only if the median delta exceeds FloorPct and every
	// per-round delta has the same sign as the median delta. Anything else is
	// not a difference this session can claim.
	Resolved bool
	// Suggestive is true when the comparison is not Resolved but every
	// per-round delta has the median delta's sign and the median delta exceeds
	// twice the larger TypicalAAPct. On a disturbed machine one outlier run
	// can set the floor and hide a consistent effect; this flags such a case
	// without claiming it. It is a reason to rerun on quiet hardware, never a
	// result.
	Suggestive bool
}

func reportArm(results []runResult, label string) armReport {
	var rates []float64
	for _, r := range results {
		if r.Arm == label+"-a" || r.Arm == label+"-b" {
			rates = append(rates, r.Rate)
		}
	}
	aa := pairedDeltas(results, label+"-a", label+"-b", nil)
	abs := make([]float64, len(aa))
	for i, d := range aa {
		abs[i] = math.Abs(d)
	}
	slices.Sort(abs)
	return armReport{Label: label, Stats: summarize(rates), FloorPct: maxAbs(aa), AADeltas: aa, TypicalAAPct: median(abs)}
}

// replicateMeans returns, per round, the mean of an arm's two replicate runs,
// for the rounds in which both exist.
func replicateMeans(results []runResult, label string) map[int]float64 {
	got := map[int]map[string]float64{}
	for _, r := range results {
		if got[r.Round] == nil {
			got[r.Round] = map[string]float64{}
		}
		got[r.Round][r.Arm] = r.Rate
	}
	out := map[int]float64{}
	for round, m := range got {
		a, okA := m[label+"-a"]
		b, okB := m[label+"-b"]
		if okA && okB {
			out[round] = (a + b) / 2
		}
	}
	return out
}

func compareArms(results []runResult, x, y armReport) comparison {
	c := comparison{
		X: x.Label, Y: y.Label,
		MedianDeltaPct: deltaPct(x.Stats.Median, y.Stats.Median),
		FloorPct:       math.Max(x.FloorPct, y.FloorPct),
	}
	mx, my := replicateMeans(results, x.Label), replicateMeans(results, y.Label)
	rounds := make([]int, 0, len(mx))
	for r := range mx {
		if _, ok := my[r]; ok {
			rounds = append(rounds, r)
		}
	}
	slices.Sort(rounds)
	for _, r := range rounds {
		c.RoundDeltas = append(c.RoundDeltas, deltaPct(mx[r], my[r]))
	}
	consistent := len(c.RoundDeltas) >= 2
	for _, d := range c.RoundDeltas {
		if (d > 0) != (c.MedianDeltaPct > 0) {
			consistent = false
		}
	}
	c.Resolved = consistent && math.Abs(c.MedianDeltaPct) > c.FloorPct
	typical := math.Max(x.TypicalAAPct, y.TypicalAAPct)
	c.Suggestive = !c.Resolved && consistent && math.Abs(c.MedianDeltaPct) > 2*typical
	return c
}

// renderMatrix renders the arm table, every pairwise comparison and the
// caveats for a matrix session.
func renderMatrix(arms []armReport, results []runResult) (string, []comparison) {
	var b strings.Builder
	b.WriteString("| arm | n | median rec/s | sd % | min | max | A/A floor (max) | A/A typical (median) |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, a := range arms {
		fmt.Fprintf(&b, "| %s | %d | %.0f | %.1f | %.0f | %.0f | ±%.1f%% | ±%.1f%% |\n",
			a.Label, a.Stats.N, a.Stats.Median, a.Stats.SDPct, a.Stats.Min, a.Stats.Max, a.FloorPct, a.TypicalAAPct)
	}
	b.WriteString("\n")

	var cmps []comparison
	b.WriteString("| comparison | median delta | per-round deltas | A/A floor | read |\n| --- | --- | --- | --- | --- |\n")
	for i := range arms {
		for j := i + 1; j < len(arms); j++ {
			c := compareArms(results, arms[i], arms[j])
			cmps = append(cmps, c)
			verdict := "not resolved"
			switch {
			case c.Resolved:
				verdict = "resolved"
			case c.Suggestive:
				verdict = "suggestive"
			}
			fmt.Fprintf(&b, "| %s vs %s | %+.1f%% | %s | ±%.1f%% | %s |\n",
				c.Y, c.X, c.MedianDeltaPct, fmtPcts(c.RoundDeltas), c.FloorPct, verdict)
		}
	}
	b.WriteString("\n")
	return b.String(), cmps
}
