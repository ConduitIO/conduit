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

package doctorcheck_test

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/root/doctor/doctorcheck"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// panickingCheck simulates a broken check.Check implementation — the kind
// doctor.go's ExecuteWithResult must survive without crashing the CLI
// process (acceptance criterion 9).
type panickingCheck struct{}

func (panickingCheck) Name() string { return "test.panics" }
func (panickingCheck) Run(context.Context) check.CheckResult {
	panic("simulated check failure")
}

// TestDoctorChecks_PanickingCheck_IsolatedAsFail is acceptance criterion 9
// at doctor's own check-running layer: doctor.go's ExecuteWithResult does
// nothing but `checks := doctorcheck.DefaultChecks(...)` followed by
// `check.Run(ctx, checks)` (see cmd/conduit/root/doctor/doctor.go) — this
// test proves that composition (a real doctor check set, plus a
// deliberately panicking Check, run through the same check.Run call
// doctor.go makes) survives a panic as a Fail result rather than a crash.
// pkg/conduit/check's own tests already cover Run's panic-isolation
// mechanism in isolation; this test is about doctor's specific integration
// with it, not a re-test of that mechanism.
func TestDoctorChecks_PanickingCheck_IsolatedAsFail(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)

	checks := doctorcheck.DefaultChecks(cfg, log.Nop(), doctorcheck.Options{})
	checks = append(checks, panickingCheck{})

	// The critical assertion is implicit: if check.Run's panic isolation
	// didn't hold, this call itself would crash the test binary instead of
	// returning — "process exits cleanly" made concrete.
	report := check.Run(context.Background(), checks)

	result := findResult(t, report, "test.panics")
	is.Equal(result.Status, check.StatusFail)
	is.Equal(result.Code, conduiterr.CodeInternal.Reason())

	// The panic in one check must not have prevented the others from
	// running and reporting their own real results.
	otherResult := findResult(t, report, doctorcheck.NamePluginsBuiltin)
	is.Equal(otherResult.Status, check.StatusPass)
}
