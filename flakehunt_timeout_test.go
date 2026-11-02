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

package conduit

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// perIterationTestBudget is go test's default -timeout, the budget each test
// binary gets in the PR `test` job, which runs the suite once (-count=1).
const perIterationTestBudget = 10 * time.Minute

// TestFlakeHuntTimeoutCoversEveryIteration: go test's -timeout is per test
// binary and spans every -count iteration. flake-hunt.yml repeats the suite
// (-count=N) and once kept the 10m default, so each iteration got a third of the
// PR job's budget and the slowest binary (pkg/plugin/processor/standalone) was
// killed on slow runners without any test hanging. The timeout must give every
// iteration at least the PR job's per-binary budget.
func TestFlakeHuntTimeoutCoversEveryIteration(t *testing.T) {
	const file = ".github/workflows/flake-hunt.yml"
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	flags := regexp.MustCompile(`(?m)^[^#\n]*GOTEST_FLAGS="([^"]*)"`).FindAllSubmatch(b, -1)
	if len(flags) == 0 {
		t.Fatalf("%s: no GOTEST_FLAGS found; update this test if the test command moved", file)
	}
	for _, m := range flags {
		f := string(m[1])
		count := 1
		if c := regexp.MustCompile(`-count[= ](\d+)`).FindStringSubmatch(f); c != nil {
			count, _ = strconv.Atoi(c[1])
		}
		timeout := perIterationTestBudget // go test's default when -timeout is absent
		if d := regexp.MustCompile(`-timeout[= ](\S+)`).FindStringSubmatch(f); d != nil {
			if timeout, err = time.ParseDuration(d[1]); err != nil {
				t.Fatalf("%s: -timeout %q: %v", file, d[1], err)
			}
		}
		if want := time.Duration(count) * perIterationTestBudget; timeout < want {
			t.Errorf("%s: GOTEST_FLAGS=%q runs each test binary %d times under a %v timeout; "+
				"go test's timeout spans all iterations, so it must be at least %v (%v per iteration)",
				file, f, count, timeout, want, perIterationTestBudget)
		}
	}
}
