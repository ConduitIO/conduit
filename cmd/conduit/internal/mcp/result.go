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
	"os"
	"path/filepath"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Result is the structured-content envelope every conduit mcp tool returns
// as CallToolResult.StructuredContent — the MCP analog of the CLI's --json
// Result envelope (docs/design-documents/20260707-cli-output-conventions.md
// §1.1): {ok, summary, result, error}. Summary and Result carry the wrapped
// engine's own report/diff/result type verbatim (validate.Report's parts,
// check.Report's parts, provisioning.Diff, scaffold.Result) — this type's
// only job is the envelope, never new domain logic (design doc §4).
type Result[T any] struct {
	OK      bool         `json:"ok"`
	Summary any          `json:"summary,omitempty"`
	Result  T            `json:"result,omitempty"`
	Error   *ErrorDetail `json:"error,omitempty"`
}

// ErrorDetail is the envelope's error shape on a domain failure: a
// *conduiterr.ConduitError's Code/Message/Suggestion/ConfigPath/Fix carried
// through as-is, so an agent gets the identical machine-actionable
// remediation data the CLI's --json envelope and ui.RenderFinding give a
// human (design doc AC-6).
type ErrorDetail struct {
	Code       string          `json:"code"`
	Message    string          `json:"message"`
	Suggestion string          `json:"suggestion,omitempty"`
	ConfigPath string          `json:"configPath,omitempty"`
	Fix        *conduiterr.Fix `json:"fix,omitempty"`
}

// toolOK builds the CallToolResult/Result pair for a call that completed
// without a HARD failure. ok is the DOMAIN verdict, not "did the Go call
// return an error" — it mirrors cecdysis.Outcome.OK exactly (see
// cmd/conduit/cecdysis/result.go's doc: "did validation find zero errors",
// not "did the command execute successfully"). validate/lint/dry_run/doctor
// pass their report's own pass/fail verdict (report.OK() /
// Summary.Errors==0 / Summary.Failed==0); deploy/apply/scaffold/inspect
// always pass true here, matching their CLI Outcome{OK: true, ...} — those
// are procedural (they either complete or hard-error), not evaluative.
//
// Returning a nil *sdkmcp.CallToolResult lets the SDK's generic
// ToolHandlerFor populate Content/StructuredContent from the returned
// Result[T] value itself (see the go-sdk's AddTool doc) — this package never
// hand-builds the success path's Content.
func toolOK[T any](ok bool, result T, summary any) (*sdkmcp.CallToolResult, Result[T], error) {
	return nil, Result[T]{OK: ok, Summary: summary, Result: result}, nil
}

// toolErr builds the CallToolResult/Result pair for a failed tool call. It
// always returns a nil error: the go-sdk's ToolHandlerFor discards the
// returned CallToolResult entirely when the handler's error return is
// non-nil (it synthesizes its own error-text-only result instead — see
// mcp.AddTool's internal toolForErr), which would lose the structured
// ErrorDetail (code/suggestion/fix) this package needs on StructuredContent.
// Returning err=nil with IsError:true set on the CallToolResult is the
// documented way to report a domain/tool-execution error while keeping full
// control of both Content and StructuredContent (design doc §4, AC-6).
func toolErr[T any](err error) (*sdkmcp.CallToolResult, Result[T], error) {
	detail := errorDetail(err)

	text := detail.Code + ": " + detail.Message
	if detail.Suggestion != "" {
		text += "\nsuggestion: " + detail.Suggestion
	}

	res := &sdkmcp.CallToolResult{
		IsError: true,
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}},
	}
	return res, Result[T]{OK: false, Error: detail}, nil
}

// errorDetail converts any error into the envelope's ErrorDetail shape: a
// *conduiterr.ConduitError's structured fields are carried through as-is
// (mirroring cecdysis.newResultError, the CLI --json envelope's equivalent
// conversion); any other error falls back to conduiterr.CodeUnknown's reason
// so every tool error still carries a stable code, never a bare message.
func errorDetail(err error) *ErrorDetail {
	if ce, ok := conduiterr.Get(err); ok {
		return &ErrorDetail{
			Code:       ce.Code.Reason(),
			Message:    ce.Message,
			Suggestion: ce.Suggestion,
			ConfigPath: ce.ConfigPath,
			Fix:        ce.Fix,
		}
	}
	return &ErrorDetail{Code: conduiterr.CodeUnknown.Reason(), Message: err.Error()}
}

// withTempConfigFile writes content to a private (0600) temp file named
// pipeline.yaml inside a fresh, single-use os.MkdirTemp directory, calls fn
// with that file's path, and always removes the directory afterward —
// including when the write itself fails or fn returns an error. This is the
// uniform content-in adapter for every offline, path-based engine
// (validate.Run/RunWithOptions, deploy.ParseSinglePipeline): an MCP agent
// supplies pipeline config CONTENT, never a path on the server host (see
// doc.go and the design doc's "one real adapter concern" — accepting an
// agent-supplied server path would let an agent read/validate arbitrary host
// files).
func withTempConfigFile[T any](content string, fn func(path string) (T, error)) (T, error) {
	var zero T

	dir, err := os.MkdirTemp("", "conduit-mcp-*")
	if err != nil {
		return zero, conduiterr.Wrap(conduiterr.CodeInternal, "could not create a temporary directory for the pipeline config", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "pipeline.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return zero, conduiterr.Wrap(conduiterr.CodeInternal, "could not write the temporary pipeline config file", err)
	}

	return fn(path)
}
