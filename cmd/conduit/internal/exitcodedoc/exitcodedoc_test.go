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

package exitcodedoc_test

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/internal/exitcodedoc"
	"github.com/conduitio/conduit/cmd/conduit/root/connectors"
	"github.com/conduitio/conduit/cmd/conduit/root/processorplugins"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/trust"
	"github.com/conduitio/ecdysis"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	bucketLine = regexp.MustCompile(`^  ([0-9])  `)
	codeLine   = regexp.MustCompile(`^ {7}([a-z0-9_]+\.[a-z0-9_.]+)\s`)
)

// documentedCodes parses the "Exit codes" block of a --help text into the
// exit code it claims for each named error code.
func documentedCodes(t *testing.T, help string) map[string]int {
	t.Helper()
	_, block, ok := strings.Cut(help, "Exit codes")
	require.True(t, ok, "help text has no Exit codes block:\n%s", help)

	got := map[string]int{}
	bucket := -1
	for _, line := range strings.Split(block, "\n") {
		if m := bucketLine.FindStringSubmatch(line); m != nil {
			bucket, _ = strconv.Atoi(m[1])
			continue
		}
		if m := codeLine.FindStringSubmatch(line); m != nil {
			require.NotEqual(t, -1, bucket, "code %s listed before any exit code", m[1])
			got[m[1]] = bucket
		}
	}
	return got
}

// TestHelpExitCodesMatchMapping is the #2907 regression test: every error
// code a command's --help lists must sit under the exit code the command
// really returns for it, and the cases the issue found misfiled must be
// documented at all.
func TestHelpExitCodesMatchMapping(t *testing.T) {
	cases := []struct {
		name     string
		cmd      ecdysis.CommandWithDocs
		required []conduiterr.Code
	}{
		{
			name: "processor-plugins install",
			cmd:  &processorplugins.InstallCommand{},
			required: []conduiterr.Code{
				registry.CodeCorruptDownload,
				trust.CodeIdentityRevoked,
				registry.CodeInvalidProcessorArtifact,
			},
		},
		{
			name:     "processor-plugins uninstall",
			cmd:      &processorplugins.UninstallCommand{},
			required: []conduiterr.Code{registry.CodeProcessorInUse},
		},
		{
			name: "connectors install",
			cmd:  &connectors.InstallCommand{},
			required: []conduiterr.Code{
				registry.CodeCorruptDownload,
				trust.CodeIdentityRevoked,
			},
		},
		{
			name:     "connectors uninstall",
			cmd:      &connectors.UninstallCommand{},
			required: []conduiterr.Code{registry.CodeConnectorInUse},
		},
		{
			name: "connectors bundle",
			cmd:  &connectors.BundleCommand{},
			required: []conduiterr.Code{
				registry.CodeCorruptDownload,
				trust.CodeIdentityRevoked,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := documentedCodes(t, tc.cmd.Docs().Long)

			for _, code := range tc.required {
				want := exitcode.ExitCode(conduiterr.New(code, "x"))
				documented, ok := got[code.Reason()]
				if assert.True(t, ok, "%s is not documented in --help", code) {
					assert.Equal(t, want, documented, "%s: --help says exit %d, the command exits %d", code, documented, want)
				}
			}
			for reason, documented := range got {
				code, ok := conduiterr.LookupCode(reason)
				if !assert.True(t, ok, "--help names unregistered code %q", reason) {
					continue
				}
				want := exitcode.ExitCode(conduiterr.New(code, "x"))
				assert.Equal(t, want, documented, "%s: --help says exit %d, the command exits %d", reason, documented, want)
			}
		})
	}
}

func TestBucket_MatchesExitCodeOnWrappedError(t *testing.T) {
	for _, code := range conduiterr.Codes() {
		wrapped := cerrors.Errorf("context: %w", conduiterr.New(code, "boom"))
		assert.Equal(t, exitcode.ExitCode(wrapped), exitcodedoc.Bucket(code), code.Reason())
	}
}

func TestRender(t *testing.T) {
	out := exitcodedoc.Render(
		exitcodedoc.Entry{Code: registry.CodeProcessorInUse, When: "in use"},
		exitcodedoc.Entry{Code: registry.CodeCorruptDownload, When: "corrupt"},
		exitcodedoc.Entry{Code: trust.CodeIdentityRevoked, When: "revoked"},
	)
	want := `Exit codes (fixed by each error code's registered category; scripts should branch on
the error code in --json output, not only on the exit code):
  0  success
  1  Runtime: internal bug, or
       registry.corrupt_download  corrupt
  2  Validation:
       registry.processor_in_use  in use
  3  Environment:
       registry.identity_revoked  revoked`
	assert.Equal(t, want, out)
}

func TestRender_RuntimeAlwaysListed(t *testing.T) {
	out := exitcodedoc.Render(exitcodedoc.Entry{Code: registry.CodeProcessorInUse, When: "in use"})
	assert.Contains(t, out, "\n  1  Runtime: internal bug\n  2  Validation:")
}

func TestRegistered(t *testing.T) {
	assert.Equal(t, registry.CodeProcessorInUse, exitcodedoc.Registered("registry.processor_in_use"))
	assert.Panics(t, func() { exitcodedoc.Registered("registry.no_such_code") })
}

func TestRender_ZeroCodePanics(t *testing.T) {
	assert.Panics(t, func() { exitcodedoc.Render(exitcodedoc.Entry{When: "x"}) })
}
