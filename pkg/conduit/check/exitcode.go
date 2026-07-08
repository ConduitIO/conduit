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
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// ExitCode reduces every Fail result in the Report to a single process exit
// code, per the CLI output conventions' multi-result rule: take the WORST
// (max) exitcode.ExitCode bucket across all failing results, not the first.
//
// This is deliberately not "classify one synthesized error": each Fail
// result is turned into its own *conduiterr.ConduitError (via
// conduiterr.LookupCode on the result's Code, falling back to
// conduiterr.CodeUnknown for an unregistered or empty Code) and classified
// independently with exitcode.ExitCode; ExitCode returns the largest bucket
// seen. Concretely: a report with one Fail whose Code maps to
// conduiterr.CodeUnavailable (environment, bucket 3) and another Fail whose
// Code maps to conduiterr.CodeInvalidArgument (validation, bucket 2) always
// returns 3, regardless of which result appears first in Report.Checks.
// cerrors.Join (or any other "join all errors, classify once" approach) is
// deliberately not used here: errors.As over a joined tree returns the
// first match in the chain, which would silently prefer whichever failure
// happened to be joined first instead of the worst one.
//
// Warn and Pass results never contribute — Report.ExitCode only escalates on
// Fail. A caller implementing `--strict` (warnings escalate to failure) must
// do that upstream, by changing the affected results' Status to StatusFail
// before calling ExitCode, not by this package inferring it.
//
// An empty Report (or one with no Fail results) returns exitcode.OK. A
// Report with at least one Fail result NEVER returns exitcode.OK, even in
// the pathological case where every failing result's Code happens to
// resolve to a gRPC category exitcode.ExitCode itself maps to OK
// (codes.OK/codes.Canceled — which no well-behaved registered
// conduiterr.Code should ever use, per that package's own registry
// discipline, but this method does not trust callers to have gotten that
// right): a Fail is definitionally not OK, so the aggregate floors at
// exitcode.Runtime in that case instead of silently reporting success.
func (r Report) ExitCode() int {
	worst := exitcode.OK
	hasFail := false

	for _, cr := range r.Checks {
		if cr.Status != StatusFail {
			continue
		}
		hasFail = true

		code, ok := conduiterr.LookupCode(cr.Code)
		if !ok {
			code = conduiterr.CodeUnknown
		}

		ce := conduiterr.New(code, cr.Message)
		if ec := exitcode.ExitCode(ce); ec > worst {
			worst = ec
		}
	}

	if hasFail && worst == exitcode.OK {
		return exitcode.Runtime
	}
	return worst
}
