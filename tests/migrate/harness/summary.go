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

package harness

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// Outcome of one harness check.
const (
	OutcomePass    = "pass"
	OutcomeFail    = "fail"
	OutcomePending = "pending"
)

// Recorder collects check outcomes for the job summary, so that pending
// checks are counted and named instead of hiding among skipped tests.
type Recorder struct {
	mu   sync.Mutex
	rows map[string]row
}

type row struct{ outcome, note string }

// Record stores the outcome of the named check.
func (r *Recorder) Record(name, outcome, note string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rows == nil {
		r.rows = map[string]row{}
	}
	r.rows[name] = row{outcome, note}
}

// Markdown renders the summary: counts, then every check that did not pass.
func (r *Recorder) Markdown() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := map[string]int{}
	names := make([]string, 0, len(r.rows))
	for n, x := range r.rows {
		counts[x.outcome]++
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "### Migration harness\n\n%d pass, %d fail, %d pending\n\n",
		counts[OutcomePass], counts[OutcomeFail], counts[OutcomePending])
	if counts[OutcomePass] < len(r.rows) {
		b.WriteString("| Check | Outcome | Note |\n| --- | --- | --- |\n")
		for _, n := range names {
			if x := r.rows[n]; x.outcome != OutcomePass {
				fmt.Fprintf(&b, "| `%s` | %s | %s |\n", n, x.outcome, strings.ReplaceAll(x.note, "|", `\|`))
			}
		}
	}
	return b.String()
}

// WriteMarkdown appends the summary to path (GITHUB_STEP_SUMMARY or a file).
func (r *Recorder) WriteMarkdown(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return cerrors.Errorf("open summary: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(r.Markdown()); err != nil {
		return cerrors.Errorf("write summary: %w", err)
	}
	return nil
}
