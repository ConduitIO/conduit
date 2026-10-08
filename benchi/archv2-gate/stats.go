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
	"slices"
)

// armStats summarizes the per-run sink rates of one arm.
type armStats struct {
	N      int
	Median float64
	Mean   float64
	// SDPct is the sample standard deviation as a percentage of the mean.
	SDPct float64
	Min   float64
	Max   float64
}

func summarize(rates []float64) armStats {
	if len(rates) == 0 {
		return armStats{}
	}
	s := slices.Clone(rates)
	slices.Sort(s)

	var sum float64
	for _, r := range s {
		sum += r
	}
	mean := sum / float64(len(s))

	var sd float64
	if len(s) > 1 {
		var sq float64
		for _, r := range s {
			sq += (r - mean) * (r - mean)
		}
		sd = math.Sqrt(sq / float64(len(s)-1))
	}

	return armStats{
		N:      len(s),
		Median: median(s),
		Mean:   mean,
		SDPct:  pct(sd, mean),
		Min:    s[0],
		Max:    s[len(s)-1],
	}
}

// median of an already sorted slice.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func pct(part, whole float64) float64 {
	if whole == 0 {
		return 0
	}
	return part / whole * 100
}

// deltaPct is (b-a)/a as a percentage.
func deltaPct(a, b float64) float64 {
	return pct(b-a, a)
}

// maxAbs returns the largest absolute value in xs, or 0 for an empty slice.
func maxAbs(xs []float64) float64 {
	var m float64
	for _, x := range xs {
		m = math.Max(m, math.Abs(x))
	}
	return m
}
