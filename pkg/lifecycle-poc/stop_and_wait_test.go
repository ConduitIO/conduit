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
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// TestServiceLifecycle_StopAndWait_Unsupported is the parity guard regression
// test called out in
// docs/design-documents/20260708-live-server-deploy-apply.md's "Open parity
// item": this (Preview.PipelineArchV2 / "funnel") lifecycle implementation's
// Stop/drain semantics have not been audited the way pkg/lifecycle's were for
// the Tier-1 live-apply review, so StopAndWait must always refuse rather than
// silently building provisioning.Service.ApplyPlanLive's stop-drain-restart
// on top of an unaudited stop path. This pins that refusal — and its stable
// error code — so a future change can't accidentally make it a silent no-op
// or delegate to Stop.
func TestServiceLifecycle_StopAndWait_Unsupported(t *testing.T) {
	is := is.New(t)

	logger := log.New(zerolog.Nop())
	ls := NewService(logger, testConnectorService{}, testProcessorService{}, testConnectorPluginService{}, testPipelineService{}, true)

	err := ls.StopAndWait(context.Background(), uuid.NewString())
	is.True(err != nil)

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeStopAndWaitUnsupported.Reason())
	is.True(ce.Suggestion != "")
}
