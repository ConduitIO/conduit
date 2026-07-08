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

package check_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/matryer/is"
)

// fnCheck adapts a name and a Run function to check.Check, for tests that
// need a bespoke check without a dedicated type.
type fnCheck struct {
	name string
	run  func(ctx context.Context) check.CheckResult
}

func (c fnCheck) Name() string                              { return c.name }
func (c fnCheck) Run(ctx context.Context) check.CheckResult { return c.run(ctx) }

// timeoutCheck additionally implements check.TimeoutCheck, so it can request
// its own per-check timeout independent of the batch default.
type timeoutCheck struct {
	fnCheck
	timeout time.Duration
}

func (c timeoutCheck) Timeout() time.Duration { return c.timeout }

// panicNameCheck panics from Name() itself, not from Run — a narrower case
// than TestRun_PanicIsolated's, since a panicking Name() can happen before
// any recover() has been registered if the runner isn't careful about
// ordering (see TestRun_PanicIsolated_FromName).
type panicNameCheck struct{}

func (panicNameCheck) Name() string { panic("name boom") }
func (panicNameCheck) Run(context.Context) check.CheckResult {
	return check.CheckResult{Status: check.StatusPass}
}

func TestRun_PanicIsolated(t *testing.T) {
	is := is.New(t)

	panicking := fnCheck{
		name: "panics",
		run: func(context.Context) check.CheckResult {
			panic("boom")
		},
	}
	fine := fnCheck{
		name: "fine",
		run: func(context.Context) check.CheckResult {
			return check.CheckResult{Status: check.StatusPass}
		},
	}

	report := check.Run(context.Background(), []check.Check{panicking, fine})

	is.Equal(len(report.Checks), 2)

	is.Equal(report.Checks[0].Name, "panics")
	is.Equal(report.Checks[0].Status, check.StatusFail)
	is.Equal(report.Checks[0].Code, "internal.error")
	is.True(strings.Contains(report.Checks[0].Message, "boom"))

	// The panic must not have prevented the next check from running (that's
	// the whole point of per-check isolation, not just "the process didn't
	// crash").
	is.Equal(report.Checks[1].Name, "fine")
	is.Equal(report.Checks[1].Status, check.StatusPass)
}

// TestRun_PanicIsolated_FromName is the regression test for a bug caught in
// review: an earlier version of runOne called c.Name() before registering
// the deferred recover(), so a panic from Name() itself escaped uncaught and
// crashed the whole Run() call — the exact failure mode per-check isolation
// exists to prevent, and worse than a panic from Run because it would also
// abort every check after the panicking one, not just that one check.
func TestRun_PanicIsolated_FromName(t *testing.T) {
	is := is.New(t)

	fine := fnCheck{
		name: "fine",
		run: func(context.Context) check.CheckResult {
			return check.CheckResult{Status: check.StatusPass}
		},
	}

	report := check.Run(context.Background(), []check.Check{panicNameCheck{}, fine})

	is.Equal(len(report.Checks), 2)
	is.Equal(report.Checks[0].Status, check.StatusFail)
	is.Equal(report.Checks[0].Code, "internal.error")
	is.True(strings.Contains(report.Checks[0].Message, "name boom"))
	is.Equal(report.Checks[0].Name, "unknown") // Name() panicked before it could report its own name

	// The panic in the first check's Name() must not have prevented the
	// second check from running at all.
	is.Equal(report.Checks[1].Name, "fine")
	is.Equal(report.Checks[1].Status, check.StatusPass)
}

func TestRun_NameDefaultedFromCheck(t *testing.T) {
	is := is.New(t)

	c := fnCheck{
		name: "unnamed-result",
		run: func(context.Context) check.CheckResult {
			// Deliberately leave Name unset on the returned result.
			return check.CheckResult{Status: check.StatusPass}
		},
	}

	report := check.Run(context.Background(), []check.Check{c})
	is.Equal(report.Checks[0].Name, "unnamed-result")
}

func TestRun_RespectsCheckOwnTimeout(t *testing.T) {
	is := is.New(t)

	c := timeoutCheck{
		fnCheck: fnCheck{
			name: "slow",
			run: func(ctx context.Context) check.CheckResult {
				select {
				case <-ctx.Done():
					return check.CheckResult{Status: check.StatusFail, Message: "timed out"}
				case <-time.After(2 * time.Second):
					return check.CheckResult{Status: check.StatusPass, Message: "finished"}
				}
			},
		},
		timeout: 20 * time.Millisecond,
	}

	start := time.Now()
	// The batch default is deliberately huge; only the check's own
	// TimeoutCheck.Timeout() should apply.
	report := check.Run(context.Background(), []check.Check{c}, check.WithTimeout(time.Hour))
	elapsed := time.Since(start)

	is.Equal(report.Checks[0].Status, check.StatusFail)
	is.Equal(report.Checks[0].Message, "timed out")
	// Generous upper bound so this isn't flaky under CI load, but tight
	// enough to prove the 20ms per-check timeout fired instead of the 1h
	// batch default (which would make this test hang for an hour).
	is.True(elapsed < 5*time.Second)
}

func TestRun_BatchDefaultTimeoutApplies(t *testing.T) {
	is := is.New(t)

	c := fnCheck{
		name: "slow",
		run: func(ctx context.Context) check.CheckResult {
			select {
			case <-ctx.Done():
				return check.CheckResult{Status: check.StatusFail, Message: "timed out"}
			case <-time.After(2 * time.Second):
				return check.CheckResult{Status: check.StatusPass, Message: "finished"}
			}
		},
	}

	report := check.Run(context.Background(), []check.Check{c}, check.WithTimeout(20*time.Millisecond))

	is.Equal(report.Checks[0].Status, check.StatusFail)
	is.Equal(report.Checks[0].Message, "timed out")
}

func TestRun_Summary(t *testing.T) {
	is := is.New(t)

	checks := []check.Check{
		fnCheck{name: "p1", run: func(context.Context) check.CheckResult { return check.CheckResult{Status: check.StatusPass} }},
		fnCheck{name: "p2", run: func(context.Context) check.CheckResult { return check.CheckResult{Status: check.StatusPass} }},
		fnCheck{name: "w1", run: func(context.Context) check.CheckResult { return check.CheckResult{Status: check.StatusWarn} }},
		fnCheck{name: "f1", run: func(context.Context) check.CheckResult { return check.CheckResult{Status: check.StatusFail} }},
	}

	report := check.Run(context.Background(), checks)

	is.Equal(report.Summary, check.Summary{Total: 4, Passed: 2, Warned: 1, Failed: 1})
}

func TestRun_Empty(t *testing.T) {
	is := is.New(t)

	report := check.Run(context.Background(), nil)
	is.Equal(len(report.Checks), 0)
	is.Equal(report.Summary, check.Summary{})
}
