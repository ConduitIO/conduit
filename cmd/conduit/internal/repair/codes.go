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

package repair

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Error codes this package registers in the single conduiterr registry
// (design doc §7). All five map to exit code 2 via pkg/conduit/exitcode's
// existing FailedPrecondition/InvalidArgument -> Validation-bucket mapping,
// consistent with deploy/apply's provisioning.CodePlanStale /
// provisioning.CodePipelineRunning.
var (
	// CodePlanStale is raised by Apply when the caller-presented hash does
	// not match the hash freshly recomputed from the current file bytes —
	// the direct analogue of provisioning.CodePlanStale (see plan.go). The
	// file changed since the plan was shown and approved.
	CodePlanStale = conduiterr.Register("repair.plan_stale", codes.FailedPrecondition)

	// CodeFixNoLongerApplies is raised per-fix (not for the whole Apply
	// call) when a selected fix's target field no longer matches what the
	// fix was computed against — either hand-edited between Collect and
	// Apply in a way the hash's per-fix granularity didn't already catch,
	// or consumed by an earlier fix in the same Apply batch (design doc §9,
	// failure mode 1 and AC-19). Other selected fixes in the same call
	// still apply; this fix is skipped, not fatal to the run.
	CodeFixNoLongerApplies = conduiterr.Register("repair.fix_no_longer_applies", codes.FailedPrecondition)

	// CodeDataPathFixRefused is raised when a FixClassDataPath fix is
	// selected for apply without the human-only Escalate path (design doc
	// §4.2, the Tier-1 crux). The CLI surfaces this as a hard refusal
	// (AC-14); MCP repair_apply — which never sets Escalate — surfaces it
	// as a per-fix skip, never a fatal call (AC-15).
	CodeDataPathFixRefused = conduiterr.Register("repair.data_path_fix_refused", codes.FailedPrecondition)

	// CodeAmbiguousFix is raised when more than one candidate fix targets
	// the same ConfigPath and the selection does not disambiguate — repair
	// never silently picks one (design doc §9, failure mode 3, AC-20).
	CodeAmbiguousFix = conduiterr.Register("repair.ambiguous_fix", codes.FailedPrecondition)

	// CodeNoFixesAvailable is raised when --apply (or repair_apply) is
	// requested but the plan contains zero appliable fixes. A read-mode
	// call with zero fixes is exit 0 ("already clean"), never this code
	// (AC-21).
	CodeNoFixesAvailable = conduiterr.Register("repair.no_fixes_available", codes.InvalidArgument)
)
