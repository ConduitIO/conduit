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

package mcp

import (
	"context"
	"os"
	"testing"

	"github.com/conduitio/conduit/pkg/scaffold"
	"github.com/matryer/is"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestScaffoldConnector_InvalidModule_ErrorCarriesCodeAndSuggestion covers
// AC-6 for a write tool: scaffold's own validate() rejection (bad --module)
// round-trips as a coded, suggestion-carrying tool error — without ever
// touching the filesystem or the toolchain preflight.
func TestScaffoldConnector_InvalidModule_ErrorCarriesCodeAndSuggestion(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{AllowMutations: true})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name: ToolScaffoldConnector,
		Arguments: map[string]any{
			"name":   "s3",
			"module": "not-a-valid-module-path",
		},
	})
	is.NoErr(err)
	is.True(res.IsError)

	env := decodeStructuredContent[Result[scaffold.Result]](t, res)
	is.True(!env.OK)
	is.True(env.Error != nil)
	is.Equal(env.Error.Code, scaffold.CodeInvalidModule.Reason())
	is.True(env.Error.Suggestion != "")
}

// TestScaffoldConnector_DestinationExists_NoOverwriteWithoutForce is a
// filesystem-touching but fast check: scaffold refuses to write over an
// existing directory unless Force is set, and does so before any toolchain
// preflight (so this test doesn't require network access or a Go
// toolchain to be meaningful).
func TestScaffoldConnector_DestinationExists_NoOverwriteWithoutForce(t *testing.T) {
	is := is.New(t)

	dir := t.TempDir() + "/conduit-connector-s3"
	is.NoErr(os.MkdirAll(dir, 0o755))

	srv := NewServer(Config{AllowMutations: true})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name: ToolScaffoldConnector,
		Arguments: map[string]any{
			"name":   "s3",
			"module": "github.com/example/conduit-connector-s3",
			"path":   dir,
		},
	})
	is.NoErr(err)
	is.True(res.IsError)

	env := decodeStructuredContent[Result[scaffold.Result]](t, res)
	is.True(env.Error != nil)
	is.Equal(env.Error.Code, scaffold.CodeDestinationExists.Reason())
}
