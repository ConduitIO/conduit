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
	"slices"
	"sort"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/matryer/is"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var readToolNames = []string{ToolValidate, ToolLint, ToolDryRun, ToolDoctor, ToolDeploy, ToolInspect}

var writeToolNames = []string{ToolApply, ToolStart, ToolStop, ToolScaffoldConnector, ToolScaffoldProcessor}

// TestNewServer_ReadToolsAlwaysRegistered is AC-1's catalog half: every read
// tool is present regardless of AllowMutations.
func TestNewServer_ReadToolsAlwaysRegistered(t *testing.T) {
	is := is.New(t)

	for _, allowMutations := range []bool{false, true} {
		srv := NewServer(Config{AllowMutations: allowMutations})
		cs := connectTestClient(t, srv)

		names := listToolNames(t, cs)
		for _, want := range readToolNames {
			is.True(slices.Contains(names, want)) // read tool must always be present
		}
	}
}

// TestNewServer_WriteToolsGatedByAllowMutations is the AC-2 regression test:
// write tools are entirely ABSENT from tool discovery without
// --allow-mutations (not merely present-and-erroring), and present with it.
// This is the safety-critical gate the design doc calls out explicitly ("the
// safety gate is theater" otherwise) — test it hard, both directions.
func TestNewServer_WriteToolsGatedByAllowMutations(t *testing.T) {
	is := is.New(t)

	t.Run("disabled by default", func(t *testing.T) {
		srv := NewServer(Config{AllowMutations: false})
		cs := connectTestClient(t, srv)
		names := listToolNames(t, cs)

		for _, want := range writeToolNames {
			is.True(!slices.Contains(names, want)) // write tool must be ABSENT, not just erroring
		}
		// Exact catalog: read tools only, nothing else leaked in.
		got := append([]string(nil), names...)
		sort.Strings(got)
		want := append([]string(nil), readToolNames...)
		sort.Strings(want)
		is.Equal(got, want)
	})

	t.Run("enabled with --allow-mutations", func(t *testing.T) {
		srv := NewServer(Config{AllowMutations: true})
		cs := connectTestClient(t, srv)
		names := listToolNames(t, cs)

		for _, want := range writeToolNames {
			is.True(slices.Contains(names, want))
		}
		for _, want := range readToolNames {
			is.True(slices.Contains(names, want))
		}
	})

	t.Run("calling apply directly without allow-mutations fails at tool lookup, not tool execution", func(t *testing.T) {
		// Belt-and-suspenders: even if a client somehow knew the name, the
		// server has genuinely never registered a handler for it — CallTool
		// must fail as "unknown tool", proving there is no hidden
		// erroring-but-present handler underneath the catalog gate.
		srv := NewServer(Config{AllowMutations: false})
		cs := connectTestClient(t, srv)

		_, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
			Name:      ToolApply,
			Arguments: map[string]any{"config": validPipelineYAML, "hash": "deadbeef"},
		})
		is.True(err != nil)
	})

	// AC-12: the same belt-and-suspenders assertion for start/stop
	// specifically — the design doc calls this out as "the gate assertion"
	// for the lifecycle tools, parity with apply above.
	t.Run("calling start directly without allow-mutations fails at tool lookup, not tool execution", func(t *testing.T) {
		srv := NewServer(Config{AllowMutations: false})
		cs := connectTestClient(t, srv)

		_, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
			Name:      ToolStart,
			Arguments: map[string]any{"pipelineId": "orders"},
		})
		is.True(err != nil)
	})

	t.Run("calling stop directly without allow-mutations fails at tool lookup, not tool execution", func(t *testing.T) {
		srv := NewServer(Config{AllowMutations: false})
		cs := connectTestClient(t, srv)

		_, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
			Name:      ToolStop,
			Arguments: map[string]any{"pipelineId": "orders"},
		})
		is.True(err != nil)
	})
}

// TestNewServer_NoAllowMutationsToolArgument is the AC-3 regression test: no
// tool in the catalog — read or write, regardless of AllowMutations — has an
// "allowMutations"/"allow_mutations" input-schema property. An agent cannot
// self-escalate by passing a parameter; the only control is the operator's
// process-level flag.
func TestNewServer_NoAllowMutationsToolArgument(t *testing.T) {
	is := is.New(t)

	for _, allowMutations := range []bool{false, true} {
		srv := NewServer(Config{AllowMutations: allowMutations})
		cs := connectTestClient(t, srv)

		res, err := cs.ListTools(context.Background(), nil)
		is.NoErr(err)

		for _, tool := range res.Tools {
			b, err := json.Marshal(tool.InputSchema)
			is.NoErr(err)
			schema := strings.ToLower(string(b))
			is.True(!strings.Contains(schema, "allowmutation")) // no such property, in any casing/spelling
		}
	}
}

// TestNewServer_Validate_StructuredResult is AC-1's per-call half for one
// representative read tool: calling validate over the in-memory transport
// returns the §1.1 structured result shape (ok/summary/result), matching the
// underlying validate.Report the CLI's own --json envelope would carry.
func TestNewServer_Validate_StructuredResult(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{})
	cs := connectTestClient(t, srv)

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      ToolValidate,
		Arguments: map[string]any{"config": validPipelineYAML},
	})
	is.NoErr(err)
	is.True(!res.IsError)

	env := decodeStructuredContent[map[string]any](t, res)
	is.Equal(env["ok"], true)

	result, ok := env["result"].(map[string]any)
	is.True(ok)
	files, ok := result["files"].([]any)
	is.True(ok)
	is.Equal(len(files), 1)
}

// decodeStructuredContent round-trips res.StructuredContent through JSON
// into T — the SDK's client-side decoding leaves StructuredContent as a
// generic `any`, so tests that need a concrete shape re-marshal/unmarshal
// rather than relying on runtime dynamic-type assertions.
func decodeStructuredContent[T any](t *testing.T, res *sdkmcp.CallToolResult) T {
	t.Helper()
	is := is.New(t)

	var out T
	b, err := json.Marshal(res.StructuredContent)
	is.NoErr(err)
	is.NoErr(json.Unmarshal(b, &out))
	return out
}
