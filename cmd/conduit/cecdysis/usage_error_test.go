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
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

// usageTestCmd takes exactly one positional argument and a required --name
// flag, so each kind of error cobra raises before RunE can be provoked.
type usageTestCmd struct {
	flags struct {
		Name string `long:"name" usage:"a required flag" required:"true"`
	}
	argErr error // returned by Args when set
	ran    bool
}

func (c *usageTestCmd) Usage() string         { return "usagetestcmd" }
func (c *usageTestCmd) ResultCommand() string { return "test.usage" }
func (c *usageTestCmd) Flags() []ecdysis.Flag { return ecdysis.BuildFlags(&c.flags) }

func (c *usageTestCmd) Args(args []string) error {
	if c.argErr != nil {
		return c.argErr
	}
	if len(args) != 1 {
		return cerrors.Errorf("requires exactly one argument, got %d", len(args))
	}
	return nil
}

func (c *usageTestCmd) ExecuteWithResult(context.Context) (cecdysis.Outcome, error) {
	c.ran = true
	return cecdysis.Outcome{OK: true}, nil
}

func (c *usageTestCmd) Render(cecdysis.Outcome) string { return "ran\n" }

// TestCommandWithResultDecorator_UsageErrors is the regression test for
// #3086: every error cobra raises before RunE (flag parsing, positional
// arguments, required flags) used to be swallowed by SilenceErrors, so the
// command printed nothing, even under --json, and exited 1. Each must now
// render the envelope (--json) or a finding (human) with a stable code and
// exit 2.
func TestCommandWithResultDecorator_UsageErrors(t *testing.T) {
	testCases := []struct {
		name     string
		args     []string
		argErr   error
		wantCode string
		wantMsg  string
	}{
		{name: "unknown flag", args: []string{"x", "--name", "n", "--bogus-flag"}, wantCode: "cli.invalid_usage", wantMsg: "unknown flag: --bogus-flag"},
		{name: "unknown flag before --json", args: []string{"x", "--name", "n", "--bogus-flag", "--json"}, wantCode: "cli.invalid_usage", wantMsg: "unknown flag: --bogus-flag"},
		{name: "bad flag value", args: []string{"x", "--name", "n", "--json=maybe"}, wantCode: "cli.invalid_usage", wantMsg: "invalid argument"},
		{name: "missing positional argument", args: []string{"--name", "n"}, wantCode: "cli.invalid_usage", wantMsg: "requires exactly one argument"},
		{name: "missing required flag", args: []string{"x"}, wantCode: "cli.invalid_usage", wantMsg: `required flag(s) "name" not set`},
		{
			name:     "coded Args error keeps its code",
			args:     []string{"x", "--name", "n"},
			argErr:   conduiterr.New(conduiterr.CodeInvalidArgument, "that input is not supported"),
			wantCode: "common.invalid_argument",
			wantMsg:  "that input is not supported",
		},
	}

	for _, tc := range testCases {
		for _, asJSON := range []bool{false, true} {
			name := tc.name + "/human"
			if asJSON {
				name = tc.name + "/json"
			}
			t.Run(name, func(t *testing.T) {
				is := is.New(t)
				args := tc.args
				hasJSON := strings.Contains(strings.Join(args, " "), "--json")
				if !asJSON && hasJSON {
					t.Skip("the case passes --json itself")
				}
				if asJSON && !hasJSON {
					args = append(append([]string{}, args...), "--json")
				}
				// The real process reads --json from os.Args when pflag
				// stopped before it; feed it the same arguments.
				defer cecdysis.SetRawArgsForTest(args)()

				c := &usageTestCmd{argErr: tc.argErr}
				cmd := newEcdysis().MustBuildCobraCommand(c)
				var out, errOut bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&errOut)
				cmd.SetArgs(args)

				err := cmd.Execute()
				is.True(err != nil)
				is.True(!c.ran)
				is.Equal(exitcode.ExitCode(err), exitcode.Validation) // usage errors are bad input: exit 2, not 1

				wantJSON := asJSON && tc.name != "bad flag value" // --json=maybe is itself the bad flag
				if wantJSON {
					is.Equal(errOut.String(), "") // nothing outside the envelope
					var res cecdysis.Result
					is.NoErr(json.Unmarshal(out.Bytes(), &res)) // exactly one JSON object on stdout
					is.Equal(res.Command, "test.usage")
					is.True(!res.OK)
					is.True(res.Error != nil)
					is.Equal(res.Error.Code, tc.wantCode)
					is.True(strings.Contains(res.Error.Message, tc.wantMsg))
					return
				}
				is.Equal(out.String(), "")
				is.True(strings.Contains(errOut.String(), tc.wantCode))
				is.True(strings.Contains(errOut.String(), tc.wantMsg))
				is.True(!strings.Contains(errOut.String(), "Error:")) // no duplicate cobra line
			})
		}
	}
}

// TestCommandWithResultDecorator_HelpIsNotAUsageError: --help must keep
// printing help and exiting 0.
func TestCommandWithResultDecorator_HelpIsNotAUsageError(t *testing.T) {
	is := is.New(t)
	defer cecdysis.SetRawArgsForTest([]string{"--help"})()
	cmd := newEcdysis().MustBuildCobraCommand(&usageTestCmd{})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--help"})
	is.NoErr(cmd.Execute())
	is.True(strings.Contains(out.String(), "--name"))
}

// TestCommandWithResultDecorator_ConfigParseErrorRenderedNotUsage: ecdysis's
// config-parse error (a malformed conduit.yaml, raised in PreRunE) is a bad
// file, not a bad command line. It must now be rendered instead of
// swallowed, but keep its uncoded classification rather than becoming
// cli.invalid_usage.
func TestCommandWithResultDecorator_ConfigParseErrorRenderedNotUsage(t *testing.T) {
	is := is.New(t)
	args := []string{"x", "--name", "n", "--json"}
	defer cecdysis.SetRawArgsForTest(args)()

	c := &usageTestCmd{argErr: cerrors.New("error parsing config: yaml: line 1: did not find expected node content")}
	cmd := newEcdysis().MustBuildCobraCommand(c)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)

	err := cmd.Execute()
	is.True(err != nil)
	_, coded := conduiterr.Get(err)
	is.True(!coded)
	is.Equal(exitcode.ExitCode(err), exitcode.Runtime) // unchanged from before #3086

	var res cecdysis.Result
	is.NoErr(json.Unmarshal(out.Bytes(), &res))
	is.True(res.Error != nil)
	is.Equal(res.Error.Code, "internal.unknown")
	is.True(strings.Contains(res.Error.Message, "error parsing config"))
}
