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

// CLI-layer tests for `conduit connectors bundle` and `install --bundle`
// wiring. The deep verification behavior (offline re-verification, tamper
// detection, yank-short-circuit, stale-bundle gating) is already covered
// exhaustively at the pkg/registry level (bundle_test.go there) against the
// real registry.TrustedVerifier — this file only proves the CLI plumbs
// flags/args through correctly and surfaces the right hard-failure codes,
// mirroring install_test.go's own "prove the wiring, not the crypto" scope.
package connectors_test

import (
	"bytes"
	"path/filepath"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/root/connectors"
	"github.com/conduitio/ecdysis"
)

func runBundle(t *testing.T, args ...string) (output string, err error) {
	t.Helper()
	e := ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
	cmd := e.MustBuildCobraCommand(&connectors.BundleCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	_, err = cmd.ExecuteC()
	return out.String(), err
}

func TestBundleArgs_MissingName(t *testing.T) {
	_, err := runBundle(t, "--json")
	require.Error(t, err)
}

func TestBundleArgs_TooManyArgs(t *testing.T) {
	_, err := runBundle(t, "widget", "extra", "--json")
	require.Error(t, err)
}

// TestBundle_IndexUnreachable_HardError proves the CLI wires IndexFile
// through and surfaces the resulting hard failure with the expected code —
// the same "prove the plumbing" scope as TestInstall_SplitsNameAndVersion.
func TestBundle_IndexUnreachable_HardError(t *testing.T) {
	out, err := runBundle(t,
		"widget",
		"--index-file="+filepath.Join(t.TempDir(), "does-not-exist.json"),
		"--output="+filepath.Join(t.TempDir(), "out.bundle.tar.gz"),
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.index_unreachable", res.Error.Code)
}

// TestInstall_BundleFlag_MissingFile proves `install --bundle` routes to
// the offline path (never reaching --index-url/--index-file resolution at
// all) and surfaces a clear hard failure for a bundle path that doesn't
// exist.
func TestInstall_BundleFlag_MissingFile(t *testing.T) {
	connectorsPath := t.TempDir()
	out, err := runInstall(t,
		"--bundle="+filepath.Join(t.TempDir(), "does-not-exist.bundle.tar.gz"),
		"--connectors.path="+connectorsPath,
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.archive_invalid", res.Error.Code)
}

// TestInstall_NoNameNoBundle_HardError proves the "requires a connector
// name ... or --bundle" validation fires when neither is given.
func TestInstall_NoNameNoBundle_HardError(t *testing.T) {
	_, err := runInstall(t, "--json")
	require.Error(t, err)
}
