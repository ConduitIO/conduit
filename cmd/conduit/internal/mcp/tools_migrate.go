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

	migratecmd "github.com/conduitio/conduit/cmd/conduit/root/migrate"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/migrate"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// KCMigrateInput is one named piece of connector-config content handed to the
// parsers. Name is a label that appears in finding locations; it is never
// opened. This is the content-in form of the CLI's file/directory/- arguments
// (design doc "Content in, content out").
type KCMigrateInput struct {
	Name    string `json:"name" jsonschema:"the input's label, used in finding locations; never opened"`
	Content string `json:"content" jsonschema:"the connector config content: a .properties file, a connector as the Connect REST API takes or returns it, a bare config map, or a REST export"`
}

// KCMigrateArgs is the kc_migrate tool's input: connector configs by content,
// plus the offline, non-mutating subset of `conduit migrate kafka-connect`'s
// flags. Not exposed, by decision (mapping design, "MCP tool parity"):
// --accept-gaps (would let an agent turn a blocking report into a running
// pipeline), --out/--force (the tool returns files, it writes nothing), and
// the network/Debezium-mutating flags that arrive in later PRs. --format maps
// to the run-level format (the CLI's --format), not a per-input hint, because
// the engine parses every input under one format.
type KCMigrateArgs struct {
	Inputs []KCMigrateInput `json:"inputs" jsonschema:"the connector configs to migrate, by content (never a server path)"`
	Worker *KCMigrateInput  `json:"worker,omitempty" jsonschema:"an optional Kafka Connect worker properties file, by content, supplying defaults (converters, client security)"`
	Format string           `json:"format,omitempty" jsonschema:"input format instead of detection: properties, json or rest-export"`
	Strict bool             `json:"strict,omitempty" jsonschema:"also fail on approximated entries and accepted gaps"`
	DryRun bool             `json:"dry_run,omitempty" jsonschema:"produce the report only and return no files"`
}

// kcMigrateFile is one output file returned content-out: the pipelines,
// REPORT.md and secrets.env.template the CLI would write under --out, keyed
// by their path relative to that directory. report.json is deliberately not
// returned — the report is the envelope's `result`.
type kcMigrateFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// kcMigrateResult is the kc_migrate tool's structured content. It is the
// same envelope every other tool returns (ok/summary/result/error), plus a
// top-level files[] carrying the outputs content-out — beside result, never
// inside it (contract #3069; the migrate harness's splitMCPFiles enforces
// this shape).
type kcMigrateResult struct {
	OK      bool              `json:"ok"`
	Summary any               `json:"summary,omitempty"`
	Result  *kcmigrate.Report `json:"result,omitempty"`
	Error   *ErrorDetail      `json:"error,omitempty"`
	Files   []kcMigrateFile   `json:"files,omitempty"`
}

// kcMigrate implements the kc_migrate (read) tool: translate Kafka Connect
// connector configs into Conduit pipelines and a compatibility report,
// content-in and content-out, writing nothing to the host. It is the same
// code path as `conduit migrate kafka-connect` — migratecmd.Run (the wrapper
// over pkg/kcmigrate/migrate.Run both callers share); the only difference is
// where the inputs come from (content vs. files) and where the outputs go
// (returned vs. --out). Always registered (Mutates: false), like lint and
// dry_run.
func (s *server) kcMigrate(ctx context.Context, _ *sdkmcp.CallToolRequest, in KCMigrateArgs) (*sdkmcp.CallToolResult, kcMigrateResult, error) {
	format, err := kcmigrate.ParseFormat(in.Format)
	if err != nil {
		return kcMigrateToolErr(err)
	}

	inputs := make([]kcmigrate.Input, len(in.Inputs))
	for i, f := range in.Inputs {
		inputs[i] = kcmigrate.Input{Name: f.Name, Content: []byte(f.Content)}
	}
	var worker *kcmigrate.Input
	if in.Worker != nil {
		worker = &kcmigrate.Input{Name: in.Worker.Name, Content: []byte(in.Worker.Content)}
	}

	res, err := migratecmd.Run(ctx, migrate.Request{
		Inputs: inputs, Worker: worker, Format: format,
		Strict: in.Strict, AcceptGaps: false,
	})
	if err != nil {
		return kcMigrateToolErr(err)
	}

	var files []kcMigrateFile
	if !in.DryRun {
		for _, f := range res.Files {
			if f.Path == migrate.ReportFile {
				continue // the report is already the envelope's result
			}
			files = append(files, kcMigrateFile{Path: f.Path, Content: string(f.Content)})
		}
	}
	return nil, kcMigrateResult{OK: res.OK, Summary: res.Report.Summary, Result: res.Report, Files: files}, nil
}

// kcMigrateToolErr is toolErr for kcMigrateResult: same CallToolResult and
// ErrorDetail, wrapped in the envelope that also carries files[] (empty).
func kcMigrateToolErr(err error) (*sdkmcp.CallToolResult, kcMigrateResult, error) {
	res, detail := toolErrResult(err)
	return res, kcMigrateResult{OK: false, Error: detail}, nil
}
