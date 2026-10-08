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
	"math"
	"testing"

	"github.com/matryer/is"
)

func TestSummarize(t *testing.T) {
	is := is.New(t)

	got := summarize([]float64{300, 100, 200, 400})
	is.Equal(got.N, 4)
	is.Equal(got.Median, 250.0)
	is.Equal(got.Mean, 250.0)
	is.Equal(got.Min, 100.0)
	is.Equal(got.Max, 400.0)
	// sample sd of {100,200,300,400} is 129.099...; as % of 250 = 51.64%
	is.True(math.Abs(got.SDPct-51.6398) < 0.001)

	odd := summarize([]float64{5, 1, 3})
	is.Equal(odd.Median, 3.0)

	one := summarize([]float64{7})
	is.Equal(one.SDPct, 0.0)

	is.Equal(summarize(nil), armStats{})
}

func TestDeltaPctAndMaxAbs(t *testing.T) {
	is := is.New(t)
	is.Equal(deltaPct(100, 103), 3.0)
	is.Equal(deltaPct(100, 95), -5.0)
	is.Equal(deltaPct(0, 5), 0.0)
	is.Equal(maxAbs([]float64{1, -4, 3}), 4.0)
	is.Equal(maxAbs(nil), 0.0)
}

func TestPlanRounds(t *testing.T) {
	is := is.New(t)

	// A/A alternates which replicate goes first, so a monotonic drift over
	// the session does not land on one replicate.
	aa := plan(sessionAAv1, 2)
	is.Equal(len(aa), 4)
	is.Equal(aa[0], runSpec{Round: 1, Arm: "v1-a", Engine: engineV1})
	is.Equal(aa[1], runSpec{Round: 1, Arm: "v1-b", Engine: engineV1})
	is.Equal(aa[2], runSpec{Round: 2, Arm: "v1-b", Engine: engineV1})
	is.Equal(aa[3], runSpec{Round: 2, Arm: "v1-a", Engine: engineV1})

	// A/B is A-B-A per round: the v1 pair brackets v2 and doubles as the
	// same-session A/A control.
	ab := plan(sessionAB, 1)
	is.Equal(ab, []runSpec{
		{Round: 1, Arm: "v1-a", Engine: engineV1},
		{Round: 1, Arm: "v2", Engine: engineV2},
		{Round: 1, Arm: "v1-b", Engine: engineV1},
	})
}

func TestPairedDeltas(t *testing.T) {
	is := is.New(t)
	results := []runResult{
		{runSpec: runSpec{Round: 1, Arm: "v1-a"}, Rate: 100},
		{runSpec: runSpec{Round: 1, Arm: "v2"}, Rate: 110},
		{runSpec: runSpec{Round: 1, Arm: "v1-b"}, Rate: 120},
		{runSpec: runSpec{Round: 2, Arm: "v1-a"}, Rate: 200},
		{runSpec: runSpec{Round: 2, Arm: "v2"}, Rate: 190},
		// round 2 has no v1-b: an interrupted round is skipped, not guessed
	}

	is.Equal(pairedDeltas(results, "v1-a", "v1-b", nil), []float64{20.0})
	is.Equal(pairedDeltas(results, "", "v2", []string{"v1-a", "v1-b"}), []float64{0.0})
	is.Equal(pairedDeltas(results, "v1-a", "v2", nil), []float64{10.0, -5.0})
}

func TestSinkPaths(t *testing.T) {
	is := is.New(t)
	got := sinkPaths([]byte(`
      - id: sink-1
        settings:
          path: /sink/sink-1.jsonl
      - id: sink-2
        settings:
          path: "/sink/sink-2.jsonl"
`))
	is.Equal(got, []string{"/sink/sink-1.jsonl", "/sink/sink-2.jsonl"})
}
