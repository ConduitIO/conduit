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

	"github.com/conduitio/conduit/pkg/scaffold"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ScaffoldArgs is the input shared by scaffold_connector/scaffold_processor.
// Kind and Language are not fields here: Kind is fixed per tool (connector
// vs. processor — two tools, not one tool with a kind argument, so an
// agent's catalog view is unambiguous about which it's invoking), and
// Language is always scaffold.LanguageGo (the only supported value today —
// see pkg/scaffold/doc.go; adding a second language earns a field here, per
// the no-speculative-generality rule).
type ScaffoldArgs struct {
	Name         string `json:"name" jsonschema:"the connector/processor's resource name (becomes the Go package name), e.g. \"s3\""`
	Module       string `json:"module" jsonschema:"the Go module path; must match github.com/<owner>/conduit-connector-<name> (or conduit-processor-<name>) for name"`
	Path         string `json:"path,omitempty" jsonschema:"destination directory; defaults to ./conduit-<kind>-<name> in the server's working directory"`
	SDKVersion   string `json:"sdkVersion,omitempty" jsonschema:"override the SDK version pinned in the embedded template snapshot"`
	SkipGenerate bool   `json:"skipGenerate,omitempty" jsonschema:"skip installing the code-gen tool and running code generation (the template ships pre-generated output)"`
	Force        bool   `json:"force,omitempty" jsonschema:"overwrite an existing destination directory"`
}

// scaffoldConnector implements the scaffold_connector (write) tool: scaffold
// a new Go connector plugin repository. Only registered when the server was
// started with --allow-mutations. Same engine as `conduit connector new`.
func (s *server) scaffoldConnector(ctx context.Context, _ *sdkmcp.CallToolRequest, in ScaffoldArgs) (*sdkmcp.CallToolResult, Result[scaffold.Result], error) {
	return s.scaffoldGenerate(ctx, scaffold.KindConnector, in)
}

// scaffoldProcessor implements the scaffold_processor (write) tool: scaffold
// a new Go (WASM) processor plugin repository. Only registered when the
// server was started with --allow-mutations. Same engine as
// `conduit processor new`.
func (s *server) scaffoldProcessor(ctx context.Context, _ *sdkmcp.CallToolRequest, in ScaffoldArgs) (*sdkmcp.CallToolResult, Result[scaffold.Result], error) {
	return s.scaffoldGenerate(ctx, scaffold.KindProcessor, in)
}

func (s *server) scaffoldGenerate(ctx context.Context, kind scaffold.Kind, in ScaffoldArgs) (*sdkmcp.CallToolResult, Result[scaffold.Result], error) {
	req := scaffold.Request{
		Kind:         kind,
		Language:     scaffold.LanguageGo,
		Name:         in.Name,
		Module:       in.Module,
		Path:         in.Path,
		SDKVersion:   in.SDKVersion,
		Git:          true,
		SkipGenerate: in.SkipGenerate,
		Force:        in.Force,
	}

	res, err := scaffold.Generate(ctx, req)
	if err != nil {
		return toolErr[scaffold.Result](err)
	}
	return toolOK(true, res, nil)
}
