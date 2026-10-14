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
	"os"
	"path/filepath"
	"testing"

	"github.com/matryer/is"
)

func TestParseArm(t *testing.T) {
	is := is.New(t)
	bin := filepath.Join(t.TempDir(), "conduit")
	is.NoErr(os.WriteFile(bin, []byte("x"), 0o700))

	a, err := parseArm("v1=v1")
	is.NoErr(err)
	is.Equal(a, armSpec{Label: "v1", Engine: engineV1})

	a, err = parseArm("v2-proto=v2:" + bin)
	is.NoErr(err)
	is.Equal(a, armSpec{Label: "v2-proto", Engine: engineV2, Binary: bin})

	a, err = parseArm("alta=v2:" + bin + "@1x1-srcbatch100")
	is.NoErr(err)
	is.Equal(a, armSpec{Label: "alta", Engine: engineV2, Binary: bin, Shape: "1x1-srcbatch100"})

	a, err = parseArm("alta=v2@1x1-srcbatch100")
	is.NoErr(err)
	is.Equal(a, armSpec{Label: "alta", Engine: engineV2, Shape: "1x1-srcbatch100"})

	for _, bad := range []string{
		"v1=v1@", "v1=v1@../x",
		"", "v1", "=v1", "v1=", "V1=v1", "v3=v3",
		"x-a=v1", // would collide with the replicate suffix
		"x-b=v1",
		"v1=v1:relative/path",
		"v1=v1:" + bin + ".missing",
	} {
		_, err := parseArm(bad)
		is.True(err != nil) // want an error for bad arm spec
	}
}

func TestValidateEnv(t *testing.T) {
	is := is.New(t)
	is.NoErr(validateEnv("CONDUIT_BENCH_DEST_ACK_LATENCY=1us"))
	is.NoErr(validateEnv("GOMAXPROCS=4"))
	is.True(validateEnv("AWS_SECRET_ACCESS_KEY=x") != nil) // outside the allowed names
	is.True(validateEnv("CONDUIT_BENCH_X") != nil)         // no value
}

func TestPlanMatrix(t *testing.T) {
	is := is.New(t)
	arms := []armSpec{
		{Label: "x", Engine: engineV1},
		{Label: "y", Engine: engineV2},
		{Label: "z", Engine: engineV2},
	}
	got := plan2(planMatrix(arms, 2))
	// A palindrome per round (a b c c b a), the leading arm rotating each round.
	is.Equal(got, []string{
		"1:x-a", "1:y-a", "1:z-a", "1:z-b", "1:y-b", "1:x-b",
		"2:y-a", "2:z-a", "2:x-a", "2:x-b", "2:z-b", "2:y-b",
	})

	// Every arm runs exactly twice per round, once as each replicate.
	count := map[string]int{}
	for _, s := range planMatrix(arms, 6) {
		count[s.Arm]++
	}
	for _, a := range arms {
		is.Equal(count[a.Label+"-a"], 6)
		is.Equal(count[a.Label+"-b"], 6)
	}
}

func plan2(specs []runSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = string(rune('0'+s.Round)) + ":" + s.Arm
	}
	return out
}

func rr(round int, arm string, rate float64) runResult {
	return runResult{runSpec: runSpec{Round: round, Arm: arm}, Rate: rate}
}

func TestCompareArms(t *testing.T) {
	is := is.New(t)

	// x is steady around 100; y is 50% higher; z is noisy around 100.
	var res []runResult
	for r, d := range []float64{0, 1, -1, 0.5, -0.5} {
		round := r + 1
		res = append(
			res,
			rr(round, "x-a", 100+d), rr(round, "x-b", 100-d),
			rr(round, "y-a", 150+d), rr(round, "y-b", 150-d),
		)
	}
	x, y := reportArm(res, "x"), reportArm(res, "y")
	is.Equal(x.Stats.N, 10)
	is.True(x.FloorPct > 1.9 && x.FloorPct < 2.1) // largest |a-b| is 2 on a mean of 100

	c := compareArms(res, x, y)
	is.True(c.MedianDeltaPct > 49.9 && c.MedianDeltaPct < 50.1)
	is.Equal(len(c.RoundDeltas), 5)
	is.True(c.Resolved)

	// A difference inside the larger floor is not resolved, however clean.
	var noisy []runResult
	for round, ab := range [][2]float64{{80, 120}, {120, 80}, {85, 115}} {
		noisy = append(
			noisy,
			rr(round+1, "x-a", ab[0]), rr(round+1, "x-b", ab[1]),
			rr(round+1, "y-a", 105), rr(round+1, "y-b", 105),
		)
	}
	c = compareArms(noisy, reportArm(noisy, "x"), reportArm(noisy, "y"))
	is.True(c.FloorPct > 30)
	is.True(!c.Resolved)

	// A median delta above the floor is still not resolved when one round
	// disagrees in sign.
	flip := []runResult{
		rr(1, "x-a", 100), rr(1, "x-b", 100), rr(1, "y-a", 120), rr(1, "y-b", 120),
		rr(2, "x-a", 100), rr(2, "x-b", 100), rr(2, "y-a", 120), rr(2, "y-b", 120),
		rr(3, "x-a", 100), rr(3, "x-b", 100), rr(3, "y-a", 90), rr(3, "y-b", 90),
	}
	c = compareArms(flip, reportArm(flip, "x"), reportArm(flip, "y"))
	is.True(c.MedianDeltaPct > 10)
	is.True(!c.Resolved)

	// An incomplete round (no -b replicate) is left out, not guessed.
	part := []runResult{rr(1, "x-a", 100), rr(1, "x-b", 100), rr(1, "y-a", 110), rr(2, "x-a", 100), rr(2, "x-b", 100), rr(2, "y-a", 110), rr(2, "y-b", 110)}
	c = compareArms(part, reportArm(part, "x"), reportArm(part, "y"))
	is.Equal(len(c.RoundDeltas), 1)
	is.True(!c.Resolved) // fewer than two complete rounds
}

func TestCheckEngineLog(t *testing.T) {
	is := is.New(t)
	v2log := "INF pipeline started\nINF using lifecycle service v2\n"
	is.NoErr(checkEngineLog(runSpec{Arm: "a", Engine: engineV2}, v2log))
	is.NoErr(checkEngineLog(runSpec{Arm: "a", Engine: engineV1}, "INF pipeline started\n"))
	// Either mismatch turns an A/B comparison into an A/A one without a sign.
	is.True(checkEngineLog(runSpec{Arm: "a", Engine: engineV2}, "INF pipeline started\n") != nil)
	is.True(checkEngineLog(runSpec{Arm: "a", Engine: engineV1}, v2log) != nil)
}

func TestCompareArmsSuggestiveWhenOneRunSetsTheFloor(t *testing.T) {
	is := is.New(t)

	// x is steady except for one disturbed run in round 3 (a 4x slowdown),
	// which sets its max floor to 75%. y is consistently 30% higher in every
	// round. The strict rule cannot resolve that; the typical difference
	// (about 1%) says a normal round was quiet.
	var res []runResult
	for round, xs := range [][2]float64{{100, 101}, {100, 99}, {100, 25}, {100, 101}, {100, 99}} {
		r := round + 1
		res = append(
			res,
			rr(r, "x-a", xs[0]), rr(r, "x-b", xs[1]),
			rr(r, "y-a", 130), rr(r, "y-b", 130),
		)
	}
	x, y := reportArm(res, "x"), reportArm(res, "y")
	is.True(x.FloorPct > 70)
	is.True(x.TypicalAAPct < 2)

	c := compareArms(res, x, y)
	is.True(!c.Resolved)
	is.True(c.Suggestive)

	// A sign flip in any round removes the suggestion too.
	res[len(res)-1].Rate = 90
	res[len(res)-2].Rate = 90
	c = compareArms(res, reportArm(res, "x"), reportArm(res, "y"))
	is.True(!c.Resolved && !c.Suggestive)
}
