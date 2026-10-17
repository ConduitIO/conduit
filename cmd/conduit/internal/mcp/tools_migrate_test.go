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
	"strings"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/matryer/is"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// unknownClassJSON is a connector of a class the rule engine does not know:
// the report carries kc.connector_class_unsupported and no pipeline is
// written, so the tool returns only REPORT.md (report.json is the envelope's
// result). Deliberately the simplest, least rule-dependent shape for the
// contract tests below.
const unknownClassJSON = `{"name": "orders", "config": {"connector.class": "com.example.Unknown", "a": "1"}}`

// s3SinkJSON is a class the engine does know: a pipeline is written (with a
// blocking kc.output_shape_differs note, so ok is still false) — it exercises
// the content-out pipeline files path.
const s3SinkJSON = `{"name": "orders-s3", "config": {
	"connector.class": "io.confluent.connect.s3.S3SinkConnector",
	"topics": "orders",
	"s3.bucket.name": "example-bucket",
	"s3.region": "us-east-1"
}}`

func kcMigrateCall(t *testing.T, cs *sdkmcp.ClientSession, args map[string]any) *sdkmcp.CallToolResult {
	t.Helper()
	is := is.New(t)
	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: ToolKCMigrate, Arguments: args})
	is.NoErr(err)
	return res
}

// TestKCMigrate_StructuredResult pins the content-out envelope: ok/summary/
// result/error at the top level, plus files[] at the top level (never nested
// inside result — contract #3069), with report.json excluded (the report is
// the envelope's result).
func TestKCMigrate_StructuredResult(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{})
	cs := connectTestClient(t, srv)

	res := kcMigrateCall(t, cs, map[string]any{
		"inputs": []map[string]any{{"name": "orders.json", "content": unknownClassJSON}},
	})
	is.True(!res.IsError) // a blocking report is a domain result, not a tool error

	env := decodeStructuredContent[map[string]any](t, res)
	is.Equal(env["ok"], false)
	is.True(env["summary"] != nil)

	result, ok := env["result"].(map[string]any)
	is.True(ok)
	is.True(result["connectors"] != nil)
	_, nested := result["files"]
	is.True(!nested) // files[] must not be inside result

	files, ok := env["files"].([]any)
	is.True(ok)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.(map[string]any)["path"].(string))
	}
	is.True(slices.Contains(paths, "REPORT.md"))
	is.True(!slices.Contains(paths, "report.json")) // result already carries the report
}

// TestKCMigrate_PipelineFilesContentOut proves a migratable class's pipeline
// file is returned content-out (files[]), not written to the host.
func TestKCMigrate_PipelineFilesContentOut(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{})
	cs := connectTestClient(t, srv)

	res := kcMigrateCall(t, cs, map[string]any{
		"inputs": []map[string]any{{"name": "orders-s3.json", "content": s3SinkJSON}},
	})
	is.True(!res.IsError)

	env := decodeStructuredContent[map[string]any](t, res)
	files, ok := env["files"].([]any)
	is.True(ok)
	var pipelinePaths []string
	for _, f := range files {
		p := f.(map[string]any)["path"].(string)
		if strings.HasPrefix(p, "pipelines/") && strings.HasSuffix(p, ".yaml") {
			pipelinePaths = append(pipelinePaths, p)
		}
	}
	is.True(len(pipelinePaths) > 0) // the migrated pipeline is returned, not written
}

// TestKCMigrate_DryRunReturnsNoFiles pins that dry_run returns the report
// only, no files, matching the CLI's --dry-run (which writes nothing).
func TestKCMigrate_DryRunReturnsNoFiles(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{})
	cs := connectTestClient(t, srv)

	res := kcMigrateCall(t, cs, map[string]any{
		"inputs":  []map[string]any{{"name": "orders.json", "content": unknownClassJSON}},
		"dry_run": true,
	})
	is.True(!res.IsError)

	env := decodeStructuredContent[map[string]any](t, res)
	_, ok := env["files"]
	is.True(!ok) // files omitted on dry_run
}

// TestKCMigrate_EmptyInputCarriesErrorCode pins that a hard failure (no
// input) returns the structured error envelope with the stable code, not a
// bare text error.
func TestKCMigrate_EmptyInputCarriesErrorCode(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{})
	cs := connectTestClient(t, srv)

	res := kcMigrateCall(t, cs, map[string]any{"inputs": []map[string]any{}})
	is.True(res.IsError)

	env := decodeStructuredContent[map[string]any](t, res)
	is.Equal(env["ok"], false)
	err, ok := env["error"].(map[string]any)
	is.True(ok)
	is.Equal(err["code"], "migrate.input_unrecognized")
}

// TestKCMigrate_SecretValueNotLeaked is a light canary for the content-out
// path: a secret literal in the input never appears in the tool's structured
// content. The strong guarantee (scrub-v1 over the returned files and report)
// is the migrate harness's job; this just pins that the MCP adapter doesn't
// add a new leak of its own.
func TestKCMigrate_SecretValueNotLeaked(t *testing.T) {
	is := is.New(t)

	srv := NewServer(Config{})
	cs := connectTestClient(t, srv)

	const secret = "hunter2-do-not-leak"
	res := kcMigrateCall(t, cs, map[string]any{
		"inputs": []map[string]any{{
			"name":    "orders.json",
			"content": `{"name": "orders", "config": {"connector.class": "com.example.Unknown", "db.password": "` + secret + `"}}`,
		}},
	})
	is.True(!res.IsError)

	env := decodeStructuredContent[map[string]any](t, res)
	all, err := json.Marshal(env)
	is.NoErr(err)
	is.True(!strings.Contains(string(all), secret))
}
