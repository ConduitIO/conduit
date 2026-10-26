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
// connector without payload:true would drop the content of every nacked record
// (only hashes are logged), so its start is refused with a coded error.
func TestService_StartRefusedWhenDLQLogsWithoutPayload(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	r := newAdmissionRun(t)

	// The harness wires a test destination as the DLQ; put the default back so
	// the refusal path is exercised.
	r.pl.DLQ = pipeline.DefaultDLQ

	err := r.ls.Start(ctx, r.pl.ID)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, pipeline.CodePipelineDLQNotRecoverable)
	is.True(ce.Suggestion != "")
}
