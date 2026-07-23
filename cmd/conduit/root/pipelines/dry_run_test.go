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

package pipelines

import (
	"bytes"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/testutils"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

// TestDryRunCommand_ValidFile_JSON is the Family A golden fixture for
// `conduit pipelines dry-run` (v0.19 workstream 8 — cli-contract.md §6
// AC-3): a clean file referencing only builtin plugins resolves cleanly and
// its --json output is a well-formed envelope.
func TestDryRunCommand_ValidFile_JSON(t *testing.T) {
	is := is.New(t)

	cmd := newValidateEcdysis().MustBuildCobraCommand(&DryRunCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"../../internal/validate/testdata/valid.yaml", "--json"})

	err := cmd.Execute()
	is.NoErr(err)

	is.NoErr(testutils.ValidateEnvelope(out.Bytes()))

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Command, "pipelines.dry-run")
	is.True(got.OK)
	is.True(got.Error == nil)
}
