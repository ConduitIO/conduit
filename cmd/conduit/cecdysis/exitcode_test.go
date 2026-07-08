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
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
	"github.com/spf13/cobra"
)

// exitCodeTestCmd is a throwaway CommandWithResult + CommandWithResultExitCode
// used to exercise the exit-code-for-a-domain-finding path: Outcome.OK ==
// false with a nil ExecuteWithResult error (a domain finding, e.g. doctor's
// failing checks) still needs to fail the process, without smuggling that
// through the HARD-failure error path (which would wrongly set the --json
// envelope's "error" field for what is not a crash).
type exitCodeTestCmd struct {
	ok   bool
	code int
}

func (c *exitCodeTestCmd) Usage() string         { return "exitcodetestcmd" }
func (c *exitCodeTestCmd) ResultCommand() string { return "test.exitcode" }

func (c *exitCodeTestCmd) ExecuteWithResult(context.Context) (cecdysis.Outcome, error) {
	return cecdysis.Outcome{OK: c.ok, Summary: map[string]any{"failed": !c.ok}}, nil
}

func (c *exitCodeTestCmd) Render(outcome cecdysis.Outcome) string {
	if outcome.OK {
		return "all checks passed\n"
	}
	return "some checks failed\n"
}

func (c *exitCodeTestCmd) ExitCode(outcome cecdysis.Outcome) int {
	if outcome.OK {
		return 0
	}
	return c.code
}

var (
	_ cecdysis.CommandWithResult         = (*exitCodeTestCmd)(nil)
	_ cecdysis.CommandWithResultExitCode = (*exitCodeTestCmd)(nil)
)

// TestCommandWithResultDecorator_DomainFailure_AnnotatesExitCode is the
// load-bearing test for CommandWithResultExitCode: a nil-error run whose
// Outcome is OK:false still records a nonzero exit code as a cobra
// Annotation on the leaf command, and the --json envelope's error stays
// null (a domain finding is not a HARD command failure).
func TestCommandWithResultDecorator_DomainFailure_AnnotatesExitCode(t *testing.T) {
	is := is.New(t)

	cmd := &exitCodeTestCmd{ok: false, code: 3}
	built := newEcdysis().MustBuildCobraCommand(cmd)
	var out bytes.Buffer
	built.SetOut(&out)
	built.SetArgs([]string{"--json"})

	leaf, err := built.ExecuteC()
	is.NoErr(err) // domain finding is not a HARD failure: err must be nil

	code, ok := cecdysis.ResultExitCode(leaf)
	is.True(ok)
	is.Equal(code, 3)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &got))
	is.Equal(got.Command, "test.exitcode")
	is.True(!got.OK)
	is.True(got.Error == nil) // domain finding, not a HARD failure
}

// TestCommandWithResultDecorator_Success_NoExitCodeAnnotation asserts a
// passing run (Outcome.OK == true) never records an exit-code annotation —
// ResultExitCode must report "not set" so cmd/conduit/cli falls through to
// exitcode.OK.
func TestCommandWithResultDecorator_Success_NoExitCodeAnnotation(t *testing.T) {
	is := is.New(t)

	cmd := &exitCodeTestCmd{ok: true, code: 3}
	built := newEcdysis().MustBuildCobraCommand(cmd)
	var out bytes.Buffer
	built.SetOut(&out)
	built.SetArgs([]string{})

	leaf, err := built.ExecuteC()
	is.NoErr(err)

	_, ok := cecdysis.ResultExitCode(leaf)
	is.True(!ok)
}

// TestCommandWithResultDecorator_DomainFailure_ZeroCodeNotAnnotated asserts
// a CommandWithResultExitCode that resolves to 0 (e.g. a bug, or a command
// that considers itself OK despite Outcome.OK being false) does not record a
// misleading annotation either — only a genuinely nonzero code is recorded.
func TestCommandWithResultDecorator_DomainFailure_ZeroCodeNotAnnotated(t *testing.T) {
	is := is.New(t)

	cmd := &exitCodeTestCmd{ok: false, code: 0}
	built := newEcdysis().MustBuildCobraCommand(cmd)
	var out bytes.Buffer
	built.SetOut(&out)
	built.SetArgs([]string{})

	leaf, err := built.ExecuteC()
	is.NoErr(err)

	_, ok := cecdysis.ResultExitCode(leaf)
	is.True(!ok)
}

func TestResultExitCode_NilAndUnset(t *testing.T) {
	is := is.New(t)

	code, ok := cecdysis.ResultExitCode(nil)
	is.Equal(code, 0)
	is.True(!ok)

	code, ok = cecdysis.ResultExitCode(&cobra.Command{})
	is.Equal(code, 0)
	is.True(!ok)
}

func TestResultExitCode_CorruptedAnnotationTreatedAsUnset(t *testing.T) {
	is := is.New(t)

	cmd := &cobra.Command{}
	cmd.Annotations = map[string]string{"conduit.exitCode": "not-a-number"}

	code, ok := cecdysis.ResultExitCode(cmd)
	is.Equal(code, 0)
	is.True(!ok)
}
