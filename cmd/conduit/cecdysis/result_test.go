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

package cecdysis_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

// resultTestCmd is a throwaway CommandWithResult used only to exercise
// CommandWithResultDecorator: a --json run emits the shared Result envelope,
// a human run calls Render, and a HARD failure (ExecuteWithResult returning
// a non-nil error) is rendered by the decorator itself with SilenceErrors
// set, so cobra never prints a second, duplicate "Error: ..." line over it.
type resultTestCmd struct {
	fail bool
}

func (c *resultTestCmd) Usage() string         { return "resulttestcmd" }
func (c *resultTestCmd) ResultCommand() string { return "test.command" }

func (c *resultTestCmd) ExecuteWithResult(context.Context) (cecdysis.Outcome, error) {
	if c.fail {
		err := conduiterr.New(conduiterr.CodeInvalidArgument, "bad input")
		err.Suggestion = "fix your input"
		err.ConfigPath = "/some/path"
		return cecdysis.Outcome{}, err
	}
	return cecdysis.Outcome{
		OK:      true,
		Summary: map[string]any{"count": 3},
		Result:  map[string]any{"detail": "all good"},
	}, nil
}

func (c *resultTestCmd) Render(outcome cecdysis.Outcome) string {
	return fmt.Sprintf("OK=%v\n", outcome.OK)
}

func newEcdysis() *ecdysis.Ecdysis {
	return ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
}

// priorFailureCmd additionally implements ecdysis.CommandWithExecute, whose
// decorator (part of ecdysis.DefaultDecorators) runs and sets cmd.RunE
// before our custom CommandWithResultDecorator gets applied — WithDecorators
// appends a decorator of a new type after the defaults, per ecdysis's own
// doc, so by the time CommandWithResultDecorator.Decorate runs, cmd.RunE
// already wraps Execute. This is exactly the "prior decorator in the chain"
// shape a future Confirm/Prompt-gated scaffold command would have.
type priorFailureCmd struct{}

func (c *priorFailureCmd) Usage() string { return "priorfailurecmd" }

func (c *priorFailureCmd) Execute(context.Context) error {
	err := conduiterr.New(conduiterr.CodeInvalidArgument, "declined")
	err.Suggestion = "answer y"
	return err
}

func (c *priorFailureCmd) ResultCommand() string { return "test.prior" }

func (c *priorFailureCmd) ExecuteWithResult(context.Context) (cecdysis.Outcome, error) {
	return cecdysis.Outcome{OK: true}, nil // must never be reached; Execute fails first
}

func (c *priorFailureCmd) Render(cecdysis.Outcome) string { return "should not render\n" }

// TestCommandWithResultDecorator_PriorDecoratorFailure_StillRendered asserts
// an error from an earlier decorator in the chain (not ExecuteWithResult
// itself) still gets the envelope/finding rendering and doesn't disappear
// silently — SilenceErrors means cobra itself would otherwise print nothing
// at all for it.
func TestCommandWithResultDecorator_PriorDecoratorFailure_StillRendered(t *testing.T) {
	is := is.New(t)

	cmd := newEcdysis().MustBuildCobraCommand(&priorFailureCmd{})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{})

	err := cmd.Execute()
	is.True(err != nil)

	is.True(strings.Contains(errOut.String(), "declined"))
	is.True(strings.Contains(errOut.String(), "answer y"))
	is.True(!strings.Contains(errOut.String(), "Error:"))
	is.True(!strings.Contains(out.String(), "should not render"))
}

// TestCommandWithResultDecorator_PriorDecoratorFailure_JSON is the --json
// variant of the same case: the envelope is still emitted, not silence.
func TestCommandWithResultDecorator_PriorDecoratorFailure_JSON(t *testing.T) {
	is := is.New(t)

	cmd := newEcdysis().MustBuildCobraCommand(&priorFailureCmd{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})

	err := cmd.Execute()
	is.True(err != nil)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Command, "test.prior")
	is.True(!got.OK)
	is.True(got.Error != nil)
	is.Equal(got.Error.Message, "declined")
}

func TestCommandWithResultDecorator_JSON_Success(t *testing.T) {
	is := is.New(t)

	cmd := newEcdysis().MustBuildCobraCommand(&resultTestCmd{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})

	is.NoErr(cmd.Execute())

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Command, "test.command")
	is.True(got.OK)
	is.True(got.Error == nil)

	summary, ok := got.Summary.(map[string]any)
	is.True(ok)
	is.Equal(summary["count"], float64(3)) // JSON numbers decode as float64
}

// TestCommandWithResultDecorator_JSON_ActuallyGoesToRealStdout is the
// regression test for a bug caught in review: cobra's Print/Println
// resolve through OutOrStderr, not OutOrStdout ("fallback to Stderr if not
// set", per cobra's own doc) — so Decorate must never call cmd.Print(ln)
// itself, only fmt.Fprint(ln)(cmd.OutOrStdout(), ...). Every other test in
// this file calls cmd.SetOut(&buf), which masks that distinction (cobra's
// getOut returns the same writer regardless of which fallback it would have
// used), exactly the way the pre-fix code passed all of them. This test
// deliberately does NOT call SetOut/SetErr — it swaps the real
// process-level os.Stdout/os.Stderr instead, matching production
// (cmd/conduit/cli never calls SetOut either), so a regression here would
// actually be caught.
func TestCommandWithResultDecorator_JSON_ActuallyGoesToRealStdout(t *testing.T) {
	is := is.New(t)

	stdoutR, stdoutW, err := os.Pipe()
	is.NoErr(err)
	stderrR, stderrW, err := os.Pipe()
	is.NoErr(err)

	origStdout, origStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutW, stderrW

	cmd := newEcdysis().MustBuildCobraCommand(&resultTestCmd{})
	cmd.SetArgs([]string{"--json"})
	execErr := cmd.Execute()

	os.Stdout, os.Stderr = origStdout, origStderr
	is.NoErr(stdoutW.Close())
	is.NoErr(stderrW.Close())

	var stdoutBuf, stderrBuf bytes.Buffer
	_, _ = io.Copy(&stdoutBuf, stdoutR)
	_, _ = io.Copy(&stderrBuf, stderrR)

	is.NoErr(execErr)
	is.Equal(stderrBuf.String(), "") // nothing at all on stderr for a success

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(stdoutBuf.Bytes(), &got))
	is.Equal(got.Command, "test.command")
	is.True(got.OK)
}

// TestCommandWithResultDecorator_JSON_SingleObject asserts --json emits
// exactly one JSON object to stdout (CLI output conventions §5) — no
// streamed human lines before or after it.
func TestCommandWithResultDecorator_JSON_SingleObject(t *testing.T) {
	is := is.New(t)

	cmd := newEcdysis().MustBuildCobraCommand(&resultTestCmd{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})

	is.NoErr(cmd.Execute())

	var decoded any
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	is.NoErr(dec.Decode(&decoded))
	// Nothing must remain after the one object (a second value, or stray
	// human-readable text, would fail this).
	_, err := dec.Token()
	is.True(err != nil) // io.EOF, i.e. exactly one JSON value was written
}

func TestCommandWithResultDecorator_JSON_HardFailure(t *testing.T) {
	is := is.New(t)

	cmd := newEcdysis().MustBuildCobraCommand(&resultTestCmd{fail: true})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--json"})

	err := cmd.Execute()
	is.True(err != nil) // the error still propagates for exit-code classification

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Command, "test.command")
	is.True(!got.OK)
	is.True(got.Error != nil)
	is.Equal(got.Error.Code, conduiterr.CodeInvalidArgument.Reason())
	is.Equal(got.Error.Message, "bad input")
	is.Equal(got.Error.Suggestion, "fix your input")
	is.Equal(got.Error.ConfigPath, "/some/path")

	// Under --json, cobra's own error line must never land on stdout or
	// stderr and corrupt/duplicate the single JSON object.
	is.True(!strings.Contains(out.String(), "Error:"))
	is.True(!strings.Contains(errOut.String(), "Error:"))
}

func TestCommandWithResultDecorator_Human_Success(t *testing.T) {
	is := is.New(t)

	cmd := newEcdysis().MustBuildCobraCommand(&resultTestCmd{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})

	is.NoErr(cmd.Execute())
	is.Equal(out.String(), "OK=true\n")
}

// TestCommandWithResultDecorator_Human_HardFailure_NoDuplicateError is the
// load-bearing test for CLI output conventions §5: the decorator sets
// SilenceErrors/SilenceUsage structurally, so a HARD failure's error text
// appears exactly once (rendered by the decorator via ui.RenderFinding), not
// twice (once from the decorator, once from cobra's default "Error: ..."
// handling, which fires whenever SilenceErrors is left false).
func TestCommandWithResultDecorator_Human_HardFailure_NoDuplicateError(t *testing.T) {
	is := is.New(t)

	cmd := newEcdysis().MustBuildCobraCommand(&resultTestCmd{fail: true})
	is.True(cmd.SilenceErrors)
	is.True(cmd.SilenceUsage)

	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{})

	err := cmd.Execute()
	is.True(err != nil)

	// cobra's own boilerplate ("Error: <msg>") must never appear — only the
	// decorator's single ui.RenderFinding-rendered line does.
	is.True(!strings.Contains(errOut.String(), "Error:"))
	is.Equal(strings.Count(errOut.String(), "bad input"), 1)
	is.True(strings.Contains(errOut.String(), "fix your input"))
}

func TestCommandWithResultDecorator_JSONFlagRegistered(t *testing.T) {
	is := is.New(t)
	cmd := newEcdysis().MustBuildCobraCommand(&resultTestCmd{})
	is.True(cmd.Flags().Lookup("json") != nil)
}
