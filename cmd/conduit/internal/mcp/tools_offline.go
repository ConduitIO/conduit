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
	"path"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ConfigArgs is the shared input for the offline, single-file pipeline
// config tools (validate, lint, dry_run): the pipeline configuration YAML
// CONTENT, never a path on the server host — see doc.go and
// withTempConfigFile.
type ConfigArgs struct {
	Config string `json:"config" jsonschema:"the pipeline configuration YAML content to check"`
}

// validate implements the validate tool: offline, static verification of a
// pipeline configuration (errors only). Same engine as
// `conduit pipelines validate` (cmd/conduit/internal/validate.Run).
func (s *server) validate(ctx context.Context, _ *sdkmcp.CallToolRequest, in ConfigArgs) (*sdkmcp.CallToolResult, Result[validate.Result], error) {
	report, err := withTempConfigFile(in.Config, func(path string) (validate.Report, error) {
		return validate.Run(ctx, path)
	})
	if err != nil {
		return toolErr[validate.Result](wrapUnresolvable(err))
	}
	return toolOK(report.OK(), validate.Result{Files: report.Files}, report.Summary)
}

// lint implements the lint tool: everything validate checks, plus the
// parser's advisory warnings. Same engine as `conduit pipelines lint`.
func (s *server) lint(ctx context.Context, _ *sdkmcp.CallToolRequest, in ConfigArgs) (*sdkmcp.CallToolResult, Result[validate.Result], error) {
	report, err := withTempConfigFile(in.Config, func(path string) (validate.Report, error) {
		return validate.RunWithOptions(ctx, path, validate.Options{Warnings: true})
	})
	if err != nil {
		return toolErr[validate.Result](wrapUnresolvable(err))
	}
	return toolOK(report.OK(), validate.Result{Files: report.Files}, report.Summary)
}

// dryRun implements the dry_run tool: everything validate checks, plus the
// fully-enriched pipeline graph `conduit run` would load, and a builtin
// -plugin existence check (always on, matching the CLI's --resolve-plugins
// default). Same engine as `conduit pipelines dry-run`.
func (s *server) dryRun(ctx context.Context, _ *sdkmcp.CallToolRequest, in ConfigArgs) (*sdkmcp.CallToolResult, Result[validate.Result], error) {
	report, err := withTempConfigFile(in.Config, func(path string) (validate.Report, error) {
		return validate.RunWithOptions(ctx, path, validate.Options{
			Enriched:       true,
			ResolvePlugins: true,
			BuiltinPlugins: builtinPluginSet(),
		})
	})
	if err != nil {
		return toolErr[validate.Result](wrapUnresolvable(err))
	}
	return toolOK(report.OK(), validate.Result{Files: report.Files}, report.Summary)
}

// wrapUnresolvable classifies validate.Run/RunWithOptions's non-nil error
// return, which per that engine's doc is reserved for a HARD failure (the
// resolved path itself couldn't be opened) — here, withTempConfigFile's own
// temp-file path, so this should only ever fire on a local I/O problem, not
// on anything about the caller-supplied content. Mirrors
// cmd/conduit/root/pipelines/validate.go's own wrap of the same error.
func wrapUnresolvable(err error) error {
	if _, ok := conduiterr.Get(err); ok {
		return err
	}
	return conduiterr.Wrap(conduiterr.CodeInvalidArgument, err.Error(), err)
}

// builtinPluginSet returns the set of resolvable builtin connector
// references, as both full module paths
// (github.com/conduitio/conduit-connector-<name>) and their short names
// (<name>), derived from builtin.DefaultBuiltinConnectors. This mirrors
// cmd/conduit/root/pipelines/dry_run.go's builtinPluginSet exactly (that
// helper is unexported to package pipelines) — kept identical so the MCP
// dry_run tool and the CLI `pipelines dry-run` command classify plugin
// references the same way; see that file if this ever needs to change.
func builtinPluginSet() map[string]struct{} {
	set := make(map[string]struct{}, len(builtin.DefaultBuiltinConnectors)*2)
	for fullPath := range builtin.DefaultBuiltinConnectors {
		set[fullPath] = struct{}{}
		set[strings.TrimPrefix(path.Base(fullPath), "conduit-connector-")] = struct{}{}
	}
	return set
}
