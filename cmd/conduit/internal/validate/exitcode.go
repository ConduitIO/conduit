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

package validate

import (
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// bucketRank orders the three pkg/conduit/exitcode buckets a Finding's code
// can fall into: Environment(3) > Validation(2) > Runtime(1) — OK/Canceled
// fold into Runtime here since a Finding, by construction, always
// represents a problem and never carries one of those two codes. This
// deliberately duplicates (a small slice of) exitcode's own
// codes.Code-to-bucket mapping rather than importing and calling it, per the
// CLI output conventions §4: "this is new aggregation logic, NOT free reuse
// of the single-error classifier; own it explicitly" — exitcode.ExitCode
// classifies one error, but a validate run must reduce N findings' worth of
// codes to the single worst one before there is an error to classify at
// all, which is a different operation exitcode has no entry point for. The
// switch is written to exhaustively cover every codes.Code value (mirroring
// exitcode.fromGRPCCode's own shape) rather than falling through a default,
// so a future grpc/codes addition fails the exhaustive linter here too,
// the same way it would in exitcode.
func bucketRank(c conduiterr.Code) int {
	switch c.GRPCCode() {
	case codes.OK, codes.Canceled:
		return 1 // Runtime — not expected on a Finding's code, see doc above
	case codes.InvalidArgument, codes.NotFound, codes.AlreadyExists,
		codes.FailedPrecondition, codes.OutOfRange:
		return 2 // Validation
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted,
		codes.Unauthenticated, codes.PermissionDenied:
		return 3 // Environment
	case codes.Internal, codes.Unknown, codes.DataLoss, codes.Aborted, codes.Unimplemented:
		return 1 // Runtime
	default:
		return 1 // Runtime
	}
}

// ExitError synthesizes a *conduiterr.ConduitError from the worst-bucket
// code across every error-severity Finding in files, or returns (nil,
// false) if there are none. It exists purely so a command's ExecuteWithResult
// has an error to hand cecdysis for classifying the process exit code (via
// pkg/conduit/exitcode) even though a validation run with findings is not
// itself a "hard command failure" per the CLI output conventions envelope
// (ok:false, error:null) — see cecdysis.Outcome.ExitErr's doc for how that
// reconciliation works structurally.
func ExitError(files []FileReport) (error, bool) {
	var worst conduiterr.Code
	haveWorst := false
	count := 0

	for _, f := range files {
		for _, find := range f.Findings {
			if find.Severity != SeverityError {
				continue
			}
			count++
			code, ok := conduiterr.LookupCode(find.Code)
			if !ok {
				code = conduiterr.CodeUnknown
			}
			if !haveWorst || bucketRank(code) > bucketRank(worst) {
				worst = code
				haveWorst = true
			}
		}
	}

	if !haveWorst {
		return nil, false
	}
	return conduiterr.New(worst, fmt.Sprintf("%d problem(s) found", count)), true
}
