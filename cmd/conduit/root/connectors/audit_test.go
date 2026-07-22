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

// Full-stack CLI integration tests for `conduit connectors audit`, mirroring
// install_test.go's pattern: build the real cobra command via ecdysis and
// drive it through cmd.ExecuteC(), proving the CLI wires the REAL
// registry.TrustedVerifier (via signTestIndex's test-only trust anchor —
// see that helper's doc comment) rather than re-testing RunAudit's own
// finding-table logic, which pkg/registry/connectoraudit_test.go already
// covers exhaustively.
package connectors_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/root/connectors"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/ecdysis"
)

// runAudit drives the real cobra command through ExecuteC and returns its
// output, the leaf command (for cecdysis.ResultExitCode — see doctor_test.go's
// identical pattern: a domain finding's nonzero exit code rides on a cobra
// Annotation, not RunE's returned error), and any HARD command failure.
func runAudit(t *testing.T, args ...string) (output string, leaf *cobra.Command, err error) {
	t.Helper()
	e := ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
	cmd := e.MustBuildCobraCommand(&connectors.AuditCommand{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	leaf, err = cmd.ExecuteC()
	return out.String(), leaf, err
}

// writeAuditManifestFixture writes a manifest.json entry directly to disk —
// audit_test.go doesn't need a real install pipeline run, just a
// pre-existing installed state (mirrors pkg/registry/uninstall_test.go's
// installFixture helper, at the CLI-test layer instead).
func writeAuditManifestFixture(t *testing.T, connectorsPath, name, version string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(connectorsPath, ".registry"), 0o755))
	artifactFile := name + "_" + version
	require.NoError(t, os.WriteFile(filepath.Join(connectorsPath, artifactFile), []byte("fixture-bytes"), 0o755))

	manifest := map[string]any{
		"schemaVersion": 1,
		"installs": map[string]any{
			name + "@" + version: map[string]any{
				"name": name, "version": version, "kind": "standalone",
				"os": "linux", "arch": "amd64", "artifactFile": artifactFile,
				"digest":      "sha256:0000000000000000000000000000000000000000000000000000000000000000",
				"installedAt": time.Now().UTC().Format(time.RFC3339), "source": "index",
				"signed": true,
			},
		},
	}
	data, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(connectorsPath, ".registry", "manifest.json"), data, 0o644))
}

func TestAudit_YankedVersion_FailsWithNonzeroExit(t *testing.T) {
	connectorsPath := t.TempDir()
	writeAuditManifestFixture(t, connectorsPath, "postgres", "0.14.0")

	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 1, Timestamp: time.Now()},
		Connectors: []index.Connector{{
			Name:      "postgres",
			Publisher: index.Publisher{ExpectedOIDCIssuer: "https://token.actions.githubusercontent.com"},
			Versions: []index.ConnectorVersion{
				{Version: "0.14.0", Yanked: &index.YankReason{Reason: "data-loss bug"}},
			},
		}},
	}
	data := signTestIndex(t, payload)
	indexPath := filepath.Join(t.TempDir(), "index.json")
	require.NoError(t, os.WriteFile(indexPath, data, 0o644))

	out, leaf, err := runAudit(t, "--connectors.path="+connectorsPath, "--index-file="+indexPath, "--json")
	require.NoError(t, err) // a Fail FINDING is not a HARD command failure — see CommandWithResultExitCode's doc

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.False(t, res.OK)
	assert.Nil(t, res.Error) // a Fail FINDING, not a hard command failure

	code, hasCode := cecdysis.ResultExitCode(leaf)
	require.True(t, hasCode)
	assert.NotZero(t, code)
}

func TestAudit_AllPass_ZeroExit(t *testing.T) {
	connectorsPath := t.TempDir()
	writeAuditManifestFixture(t, connectorsPath, "postgres", "0.14.1")

	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 1, Timestamp: time.Now()},
		Connectors: []index.Connector{{
			Name:      "postgres",
			Publisher: index.Publisher{ExpectedOIDCIssuer: "https://token.actions.githubusercontent.com"},
			Versions:  []index.ConnectorVersion{{Version: "0.14.1"}},
		}},
	}
	data := signTestIndex(t, payload)
	indexPath := filepath.Join(t.TempDir(), "index.json")
	require.NoError(t, os.WriteFile(indexPath, data, 0o644))

	out, leaf, err := runAudit(t, "--connectors.path="+connectorsPath, "--index-file="+indexPath, "--json")
	require.NoError(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.True(t, res.OK)

	_, hasCode := cecdysis.ResultExitCode(leaf)
	assert.False(t, hasCode, "an all-pass audit must not record a nonzero exit code annotation")
}

// TestAudit_IndexUnreachable_HardFailsDistinctly is AC8 at the CLI layer.
func TestAudit_IndexUnreachable_HardFailsDistinctly(t *testing.T) {
	connectorsPath := t.TempDir()
	writeAuditManifestFixture(t, connectorsPath, "postgres", "0.14.1")

	out, _, err := runAudit(t,
		"--connectors.path="+connectorsPath,
		"--index-file="+filepath.Join(t.TempDir(), "does-not-exist.json"),
		"--json",
	)
	require.Error(t, err)

	var res cecdysis.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.NotNil(t, res.Error)
	assert.Equal(t, "registry.index_unreachable", res.Error.Code)
}
