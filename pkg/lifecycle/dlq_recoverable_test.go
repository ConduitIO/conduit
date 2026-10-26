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
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/matryer/is"
)

// TestService_StartRefusedWhenDLQLogsWithoutPayload is the regression test for
// GHSA-m3mw-fpw8-w765's DLQ half: a pipeline whose DLQ is the built-in log
// connector without payload:true, and which routes nacked records to it, would
// drop the content of every nacked record (only hashes are logged), so its
// start is refused with a coded error.
func TestService_StartRefusedWhenDLQLogsWithoutPayload(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t)

	// The harness wires a test destination as the DLQ; put a log DLQ that
	// tolerates one nack in its place so the refusal path is exercised.
	r.pl.DLQ = pipeline.DefaultDLQ
	r.pl.DLQ.WindowNackThreshold = 1

	err := r.ls.Start(ctx, r.pl.ID)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, pipeline.CodePipelineDLQNotRecoverable)
	is.True(ce.Suggestion != "")
}

// TestService_DefaultDLQNotRefused is the regression test for the v0.21.0
// refusal of every pipeline without a DLQ block: the default DLQ routes no
// nacked record (threshold 0 stops the pipeline on the first nack and the
// record is redelivered), so it loses nothing and must pass the guard.
func TestService_DefaultDLQNotRefused(t *testing.T) {
	is := is.New(t)
	r := newAdmissionRun(t)
	r.pl.DLQ = pipeline.DefaultDLQ
	is.NoErr(r.ls.checkDLQRecoverable(r.pl))
}
