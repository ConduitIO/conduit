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

package cecdysis

import (
	"context"

	"github.com/conduitio/conduit/cmd/conduit/internal/ui"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/ecdysis"
	"github.com/spf13/cobra"
)

// Result is the shared --json envelope every v0.17 "CLI as product" command
// (pipelines validate|lint|dry-run, doctor, connector|processor new) emits,
// per the CLI output conventions §1. It is marshaled lowerCamelCase, matching
// existing protojson output.
//
// Command, OK and Error are always present (Error is null on success) so an
// agent can write one success check (.ok) and one error check (.error) for
// every command that uses this envelope. Summary and Result are
// command-specific payloads.
type Result struct {
	// Command is the stable dotted discriminator, e.g. "pipelines.validate".
	Command string `json:"command"`
	// OK is the run's verdict. This is the domain outcome (e.g. "did
	// validation find zero errors"), not "did the command execute
	// successfully" — a validation run that finds problems is OK:false with
	// Error:nil; the run itself succeeded.
	OK bool `json:"ok"`
	// Summary is a command-specific count/rollup payload.
	Summary any `json:"summary"`
	// Result is the command-specific detail payload.
	Result any `json:"result"`
	// Error is set on a HARD command failure (bad input, preflight failure,
	// crash) — never for domain findings, which belong in Summary/Result
	// with OK:false instead. Nil (JSON null) on success.
	Error *ResultError `json:"error"`
}

// ResultError is the envelope's error shape. It mirrors
// conduiterr.ConduitError's structured fields (Code, Message, Suggestion,
// ConfigPath, Fix) so a --json consumer gets the same remediation data a
// human sees rendered via ui.RenderFinding.
type ResultError struct {
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	ConfigPath string `json:"configPath,omitempty"`
	// Fix is a structured, machine-appliable fix (conduiterr.Fix), when the
	// underlying error carries one.
	Fix any `json:"fix,omitempty"`
}

// Outcome is what CommandWithResult.ExecuteWithResult returns on a
// successful run — the command completed; OK/Summary/Result carry the
// domain verdict and findings. A non-nil error return from
// ExecuteWithResult, not a false OK here, is what signals a HARD command
// failure (see CommandWithResult's doc).
type Outcome struct {
	OK      bool
	Summary any
	Result  any
}

// CommandWithResult can be implemented by a command that needs the shared
// --json envelope but does NOT dial the Conduit API client — the offline
// sibling of CommandWithExecuteWithClientResult, for commands like `doctor`
// and `pipelines validate` whose own checks are the thing being run, not a
// client call. Use CommandWithExecuteWithClientResult instead for a command
// that does need a client.
type CommandWithResult interface {
	ecdysis.Command

	// ResultCommand returns the stable dotted discriminator for the
	// envelope's "command" field, e.g. "pipelines.validate".
	ResultCommand() string
	// ExecuteWithResult performs the command's work. A non-nil error is a
	// HARD command failure and populates the envelope's Error field, NOT
	// domain findings — a validation run that found problems returns
	// Outcome{OK: false, ...}, err: nil.
	ExecuteWithResult(ctx context.Context) (Outcome, error)
	// Render returns the human-readable rendering of a successful run's
	// Outcome. Not called under --json, and not called on a HARD failure
	// (the decorator renders that itself via ui.RenderFinding, since there
	// is no Outcome to hand Render in that case).
	Render(outcome Outcome) string
}

// CommandWithResultDecorator wires CommandWithResult's --json flag, renders
// the shared Result envelope (JSON) or Outcome (human), and — structurally,
// for every command it decorates, per CLI output conventions §5 — sets
// SilenceErrors and SilenceUsage so cobra never prints its own duplicate
// "Error: ..." line over the command's own rendering, and never corrupts a
// --json run's single JSON object with a stderr error line.
type CommandWithResultDecorator struct{}

func (CommandWithResultDecorator) Decorate(_ *ecdysis.Ecdysis, cmd *cobra.Command, c ecdysis.Command) error {
	v, ok := c.(CommandWithResult)
	if !ok {
		return nil
	}

	if cmd.Flags().Lookup("json") == nil {
		cmd.Flags().Bool("json", false, "output the result as JSON")
	}

	// Structural, not opt-in: every command that uses this decorator gets
	// this, per CLI output conventions §5 ("Fold this into the shared
	// offline CommandWithResult decorator so it is structural").
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	old := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		asJSON, _ := cmd.Flags().GetBool("json")

		// A prior decorator in the chain (e.g. a future confirm/prompt step
		// on a scaffold command) can fail before ExecuteWithResult ever
		// runs. SilenceErrors above means cobra will not print anything for
		// that error on its own, so it gets the same rendering treatment as
		// an ExecuteWithResult failure — otherwise a declined confirmation
		// would exit nonzero with no output at all, under both --json and
		// human mode.
		if old != nil {
			if err := old(cmd, args); err != nil {
				renderResultError(cmd, v.ResultCommand(), err, asJSON)
				return err
			}
		}

		ctx := ecdysis.ContextWithCobraCommand(cmd.Context(), cmd)

		outcome, err := v.ExecuteWithResult(ctx)
		if err != nil {
			renderResultError(cmd, v.ResultCommand(), err, asJSON)
			// Preserve err so the process exit code (pkg/conduit/exitcode,
			// wired in cmd/conduit/cli) still classifies it correctly;
			// SilenceErrors above stops cobra from printing it a second time.
			return err
		}

		if asJSON {
			res := Result{Command: v.ResultCommand(), OK: outcome.OK, Summary: outcome.Summary, Result: outcome.Result}
			b, merr := marshalJSON(res)
			if merr != nil {
				return cerrors.Errorf("could not marshal result to JSON: %w", merr)
			}
			cmd.Println(string(b))
			return nil
		}

		cmd.Print(v.Render(outcome))
		return nil
	}

	return nil
}

// renderResultError renders a HARD command failure: the single JSON object
// (with Error set) under --json, or a located finding via ui.RenderFinding
// on stderr otherwise. It never returns an error itself — marshaling
// ResultError can't fail (it's a plain struct of strings and an any Fix that
// is itself always JSON-safe, being either nil or a conduiterr.Fix), so a
// marshal failure here would indicate a bug, not a runtime condition to
// propagate.
func renderResultError(cmd *cobra.Command, command string, err error, asJSON bool) {
	re := newResultError(err)

	if asJSON {
		res := Result{Command: command, OK: false, Error: re}
		b, merr := marshalJSON(res)
		if merr != nil {
			// Should not happen (see doc above); fall back to something
			// rather than emitting nothing on stdout under --json.
			cmd.PrintErrln("Error:", err)
			return
		}
		cmd.Println(string(b))
		return
	}

	noColor, _ := cmd.Flags().GetBool("no-color")
	out := cmd.ErrOrStderr()
	r := ui.NewRenderer(out, noColor)
	ui.RenderFinding(out, r.Glyph(ui.StatusFail), re.Code, re.ConfigPath, re.Message, re.Suggestion)
}

// newResultError converts a plain error into the envelope's ResultError
// shape: a *conduiterr.ConduitError's structured fields are carried through
// as-is; any other error falls back to conduiterr.CodeUnknown's reason with
// just a message, so every hard failure still carries a stable code.
func newResultError(err error) *ResultError {
	if ce, ok := conduiterr.Get(err); ok {
		var fix any
		if ce.Fix != nil {
			fix = ce.Fix
		}
		return &ResultError{
			Code:       ce.Code.Reason(),
			Message:    ce.Message,
			Suggestion: ce.Suggestion,
			ConfigPath: ce.ConfigPath,
			Fix:        fix,
		}
	}
	return &ResultError{Code: conduiterr.CodeUnknown.Reason(), Message: err.Error()}
}
