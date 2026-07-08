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
	"testing"

	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
	"google.golang.org/grpc/codes"
)

func TestReport_ExitCode_Empty(t *testing.T) {
	is := is.New(t)
	is.Equal(check.Report{}.ExitCode(), exitcode.OK)
}

// canceledTestCode is registered at package-var-init time, per
// conduiterr.Register's own documented invariant ("MUST be called only from
// a package-level var initializer... calling Register after program start
// or from a goroutine is a data race") — not inside the test function body
// that uses it, matching the convention pkg/conduit/exitcode's own test
// suite (testCodes) already establishes.
var canceledTestCode = conduiterr.Register("test.check.exitcode.canceled", codes.Canceled)

// TestReport_ExitCode_FailNeverReportsOK guards the pathological case where
// a Fail result's Code resolves to a gRPC category exitcode.ExitCode itself
// treats as OK (codes.Canceled) — something no well-behaved registered
// conduiterr.Code should do, but this method must not trust that. A Fail is
// definitionally not OK; ExitCode must floor at exitcode.Runtime instead of
// silently reporting success.
func TestReport_ExitCode_FailNeverReportsOK(t *testing.T) {
	is := is.New(t)

	r := check.Report{Checks: []check.CheckResult{
		{Name: "misbehaving", Status: check.StatusFail, Code: canceledTestCode.Reason()},
	}}

	is.Equal(r.ExitCode(), exitcode.Runtime)
}

func TestReport_ExitCode_AllPass(t *testing.T) {
	is := is.New(t)

	r := check.Report{Checks: []check.CheckResult{
		{Name: "a", Status: check.StatusPass},
		{Name: "b", Status: check.StatusPass},
	}}
	is.Equal(r.ExitCode(), exitcode.OK)
}

// TestReport_ExitCode_WarnDoesNotEscalate asserts a Warn-only report exits
// OK: only Fail results feed the aggregation (a `--strict` flag that wants
// warnings to fail the run is the caller's job, not this package's).
func TestReport_ExitCode_WarnDoesNotEscalate(t *testing.T) {
	is := is.New(t)

	r := check.Report{Checks: []check.CheckResult{
		{Name: "a", Status: check.StatusWarn, Code: conduiterr.CodeUnavailable.Reason()},
	}}
	is.Equal(r.ExitCode(), exitcode.OK)
}

// TestReport_ExitCode_UnregisteredCodeFallsBackToUnknown asserts a Fail
// result whose Code isn't a registered conduiterr.Code reason still
// classifies (as Runtime, via conduiterr.CodeUnknown) instead of panicking
// or silently being skipped.
func TestReport_ExitCode_UnregisteredCodeFallsBackToUnknown(t *testing.T) {
	is := is.New(t)

	r := check.Report{Checks: []check.CheckResult{
		{Name: "a", Status: check.StatusFail, Code: "not.a.registered.reason"},
	}}
	is.Equal(r.ExitCode(), exitcode.Runtime)
}

// TestReport_ExitCode_MaxNotFirst is the load-bearing test for the ADR's
// aggregation rule: given a Fail@Unavailable (environment, bucket 3) and a
// Fail@InvalidArgument (validation, bucket 2) in the same Report, ExitCode
// must return 3 — the WORST bucket — regardless of which result appears
// first in Report.Checks. A "classify the first error" implementation (e.g.
// cerrors.Join + a single exitcode.ExitCode call, which errors.As would
// resolve to whichever error was joined first) would return 2 when the
// validation failure happens to be listed first; this test fails that
// implementation in one of its two orderings.
func TestReport_ExitCode_MaxNotFirst(t *testing.T) {
	envFail := check.CheckResult{
		Name:   "env",
		Status: check.StatusFail,
		Code:   conduiterr.CodeUnavailable.Reason(), // -> exitcode.Environment (3)
	}
	validationFail := check.CheckResult{
		Name:   "validation",
		Status: check.StatusFail,
		Code:   conduiterr.CodeInvalidArgument.Reason(), // -> exitcode.Validation (2)
	}

	// Sanity check the two codes actually land in the buckets this test
	// depends on, so a future change to the registry's gRPC categories fails
	// loudly here instead of silently degrading this test to a tautology.
	is := is.New(t)
	is.Equal(exitcode.ExitCode(conduiterr.New(conduiterr.CodeUnavailable, "x")), exitcode.Environment)
	is.Equal(exitcode.ExitCode(conduiterr.New(conduiterr.CodeInvalidArgument, "x")), exitcode.Validation)

	t.Run("environment_first", func(t *testing.T) {
		is := is.New(t)
		r := check.Report{Checks: []check.CheckResult{envFail, validationFail}}
		is.Equal(r.ExitCode(), exitcode.Environment)
	})

	t.Run("validation_first", func(t *testing.T) {
		is := is.New(t)
		r := check.Report{Checks: []check.CheckResult{validationFail, envFail}}
		is.Equal(r.ExitCode(), exitcode.Environment)
	})
}

// TestReport_ExitCode_MaxAcrossAllThreeBuckets exercises a Report carrying
// one Fail per non-OK bucket (Runtime, Validation, Environment) in every
// permutation of Checks order, asserting ExitCode always returns Environment
// (the max of {1,2,3}).
func TestReport_ExitCode_MaxAcrossAllThreeBuckets(t *testing.T) {
	runtimeFail := check.CheckResult{Name: "runtime", Status: check.StatusFail, Code: conduiterr.CodeInternal.Reason()}
	validationFail := check.CheckResult{Name: "validation", Status: check.StatusFail, Code: conduiterr.CodeInvalidArgument.Reason()}
	envFail := check.CheckResult{Name: "env", Status: check.StatusFail, Code: conduiterr.CodeUnavailable.Reason()}

	orderings := map[string][]check.CheckResult{
		"runtime_validation_env": {runtimeFail, validationFail, envFail},
		"runtime_env_validation": {runtimeFail, envFail, validationFail},
		"validation_runtime_env": {validationFail, runtimeFail, envFail},
		"validation_env_runtime": {validationFail, envFail, runtimeFail},
		"env_runtime_validation": {envFail, runtimeFail, validationFail},
		"env_validation_runtime": {envFail, validationFail, runtimeFail},
	}

	for name, checks := range orderings {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			r := check.Report{Checks: checks}
			is.Equal(r.ExitCode(), exitcode.Environment)
		})
	}
}
