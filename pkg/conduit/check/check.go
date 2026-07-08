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

package check

import (
	"context"
	"fmt"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// Status is a check's verdict.
type Status string

const (
	// StatusPass means the check found nothing wrong.
	StatusPass Status = "pass"
	// StatusWarn means the check found something worth surfacing, but not
	// severe enough to fail the run. A warning never contributes to
	// Report.ExitCode on its own; a caller that wants warnings to escalate
	// (e.g. a `--strict` flag) owns that decision, not this package.
	StatusWarn Status = "warn"
	// StatusFail means the check found a problem that should fail the run.
	// Report.ExitCode aggregates exactly the Fail results.
	StatusFail Status = "fail"
)

// Category groups CheckResults by the kind of thing they diagnose (e.g. for
// human output grouping). These are the categories the generic constructors
// in this package use; a caller supplying its own Checks (doctor's
// config/store/network/plugin checks, for instance) is free to use its own
// category strings.
const (
	CategoryToolchain  = "toolchain"
	CategoryFilesystem = "filesystem"
	CategoryNetwork    = "network"
)

// CheckResult is one check's outcome. Field set matches the CLI output
// conventions' located-finding shape (see
// docs/design-documents/20260707-cli-output-conventions.md): Code,
// ConfigPath and Suggestion are what a `--json` consumer or the shared
// cmd/conduit/internal/ui renderer key off of.
type CheckResult struct {
	// Name is the check's stable identifier. The runner fills this in from
	// the originating Check.Name() if a Check's Run leaves it empty.
	Name string `json:"name"`
	// Status is the check's verdict.
	Status Status `json:"status"`
	// Message is a human-readable description of what the check found.
	Message string `json:"message,omitempty"`
	// Suggestion is a human-readable remediation hint, optional.
	Suggestion string `json:"suggestion,omitempty"`
	// Code is a registered conduiterr.Code reason (e.g. "common.unavailable"),
	// set on Warn/Fail results so a --json consumer and Report.ExitCode have
	// a stable, machine-actionable identity to key off of. Optional on Pass.
	Code string `json:"code,omitempty"`
	// ConfigPath is the conduit.yaml dotted key (or other config address
	// space the caller uses) this result concerns, optional.
	ConfigPath string `json:"configPath,omitempty"`
	// Category groups this result for human output (e.g. "toolchain",
	// "filesystem", "network"), optional.
	Category string `json:"category,omitempty"`
}

// Check is a single diagnostic. Implementations should be side-effect-free
// beyond the diagnostic itself (e.g. DirWritable creates and immediately
// removes a probe file; it does not leave state behind).
type Check interface {
	// Name is the check's stable identifier. It must be available without
	// running the check, since the runner uses it to label a result when Run
	// panics before returning one.
	Name() string
	// Run executes the check. Implementations that do I/O (exec a binary,
	// dial a network address, touch a filesystem path) should pass ctx
	// through so the runner's per-check timeout (see Run) is honored.
	Run(ctx context.Context) CheckResult
}

// TimeoutCheck is implemented by a Check that needs a timeout different from
// the runner's default (see WithTimeout). Run checks for this interface
// before falling back to the batch default, so a slow-by-nature check (a
// network probe) can ask for a longer budget than a fast one without
// changing the timeout for every other check in the same Report.
type TimeoutCheck interface {
	Check
	// Timeout returns the maximum duration this check's Run is allowed to
	// block. A duration <= 0 is treated the same as not implementing this
	// interface (the batch default applies).
	Timeout() time.Duration
}

// DefaultTimeout is the per-check timeout Run applies when neither a
// TimeoutCheck override nor WithTimeout sets one.
const DefaultTimeout = 10 * time.Second

// Summary is the aggregate count across a Report's CheckResults.
type Summary struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
	Warned int `json:"warned"`
	Failed int `json:"failed"`
}

// Report is the result of running a set of Checks.
type Report struct {
	Checks  []CheckResult `json:"checks"`
	Summary Summary       `json:"summary"`
}

// runConfig holds Run's options.
type runConfig struct {
	timeout time.Duration
}

// Option configures Run.
type Option func(*runConfig)

// WithTimeout sets the default per-check timeout for a Run call. It applies
// to every Check in the batch that does not implement TimeoutCheck. The
// zero value (or omitting this option) uses DefaultTimeout.
func WithTimeout(d time.Duration) Option {
	return func(c *runConfig) { c.timeout = d }
}

// Run executes every Check and collects the results into a Report. It never
// panics and never blocks past a check's timeout budget:
//
//   - Per-check recover(): a panicking Check.Run is converted into a
//     StatusFail CheckResult (Code: conduiterr.CodeInternal's reason, i.e.
//     "internal.error") instead of crashing the calling process. This
//     isolation covers the panic happening synchronously inside Run; it does
//     NOT cover a panic in a goroutine a Check spawns and does not join back
//     before returning — Go's runtime has no way to recover across a
//     goroutine boundary. A Check with that shape (e.g. a future check that
//     dispenses a go-plugin, per the ADR's noted limitation) must isolate
//     itself, e.g. by running in a subprocess with its own timeout and
//     treating an abnormal child exit as Fail.
//   - Per-check context timeout: each Check.Run is given a context derived
//     from ctx with a deadline — the check's own TimeoutCheck.Timeout() if it
//     implements that interface, else the Run call's WithTimeout option, else
//     DefaultTimeout. A Check must itself honor ctx (pass it to
//     exec.CommandContext, net.Dial, etc.) for the timeout to actually bound
//     its execution; Run cannot force-abandon a check that ignores ctx and
//     never returns.
//
// Checks run sequentially, in the order given. Ordering only affects
// Report.Checks; it never affects Report.ExitCode (see its doc).
func Run(ctx context.Context, checks []Check, opts ...Option) Report {
	cfg := runConfig{timeout: DefaultTimeout}
	for _, opt := range opts {
		opt(&cfg)
	}

	results := make([]CheckResult, 0, len(checks))
	for _, c := range checks {
		results = append(results, runOne(ctx, c, cfg.timeout))
	}

	return Report{Checks: results, Summary: summarize(results)}
}

// runOne runs a single check with panic isolation and a per-check timeout.
func runOne(ctx context.Context, c Check, defaultTimeout time.Duration) (result CheckResult) {
	name := c.Name()

	defer func() {
		if r := recover(); r != nil {
			result = CheckResult{
				Name:    name,
				Status:  StatusFail,
				Code:    conduiterr.CodeInternal.Reason(),
				Message: fmt.Sprintf("check %q panicked: %v", name, r),
			}
		}
	}()

	timeout := defaultTimeout
	if tc, ok := c.(TimeoutCheck); ok {
		if t := tc.Timeout(); t > 0 {
			timeout = t
		}
	}

	checkCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		checkCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	result = c.Run(checkCtx)
	if result.Name == "" {
		result.Name = name
	}
	return result
}

// summarize computes a Summary over results.
func summarize(results []CheckResult) Summary {
	s := Summary{Total: len(results)}
	for _, r := range results {
		switch r.Status {
		case StatusPass:
			s.Passed++
		case StatusWarn:
			s.Warned++
		case StatusFail:
			s.Failed++
		}
	}
	return s
}
