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

package provisioning

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodePipelineIDDuplicate is raised when two pipeline documents — in the same
// file or across files — declare the same pipeline ID.
//
// It is distinct from config.CodeIDDuplicate, which config.Validate raises
// for a connector/processor ID collision *within* a single pipeline
// document: that check has the whole document in hand. A pipeline-ID
// collision spans documents (and files), which config.Validate never sees
// (it validates one config.Pipeline at a time) — detecting it is the job of
// whatever collects pipelines across a directory: today that's
// Service.findDuplicateIDs (a bare sentinel, ErrDuplicatedPipelineID, since
// it only needs to skip the offending pipelines during provisioning) and the
// offline `conduit pipelines validate` engine (cmd/conduit/internal/validate,
// which needs a real code + configPath per finding for its report).
var CodePipelineIDDuplicate = conduiterr.Register("provisioning.pipeline_id_duplicate", codes.InvalidArgument)

// CodePlanStale is raised by ApplyPlan when the caller-presented plan hash
// does not match the hash of the freshly recomputed Plan — the config file
// or the live pipeline state changed since the plan was shown and approved.
// See docs/design-documents/20260708-cli-pipeline-deploy-apply.md §2 ("Token
// replay / TOCTOU"): this is the safety gate that makes the plan-hash token
// real rather than theater.
var CodePlanStale = conduiterr.Register("provisioning.plan_stale", codes.FailedPrecondition)

// CodePipelineRunning is raised by ApplyPlan when it refuses to mutate a
// pipeline that is currently running (Invariant 7 — see plan.go's
// isRunningStatus and the design doc's AC-13). The fix is on the caller's
// side (stop the pipeline first), matching FailedPrecondition's exit-code-2
// classification (pkg/conduit/exitcode), not an environment failure.
var CodePipelineRunning = conduiterr.Register("provisioning.pipeline_running", codes.FailedPrecondition)
