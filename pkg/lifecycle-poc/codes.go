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

package lifecycle

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeStopAndWaitUnsupported is raised by StopAndWait, which this
// (Preview.PipelineArchV2 / "funnel") lifecycle implementation does not yet
// provide. See StopAndWait's doc for why: the sibling pkg/lifecycle package's
// Stop/WaitPipeline drain semantics were audited for the Tier-1 live-apply
// review (docs/design-documents/20260708-live-server-deploy-apply.md, "Open
// parity item") and found safe to build StopAndWait on top of; this
// experimental worker/funnel architecture has not had the equivalent audit.
// Rather than guess at its drain semantics, StopAndWait refuses outright so
// provisioning.Service.ApplyPlanLive can never apply-to-running under an
// unaudited stop path.
var CodeStopAndWaitUnsupported = conduiterr.Register("lifecycle_v2.stop_and_wait_unsupported", codes.Unimplemented)
