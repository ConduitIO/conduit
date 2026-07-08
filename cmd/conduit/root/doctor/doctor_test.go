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

// These are the full-stack integration tests for `conduit doctor`: they
// build the real cobra command (via ecdysis, exactly like cmd/conduit/cli
// does) and drive it through cmd.ExecuteC(), asserting on both the
// process-level exit code (via cecdysis.ResultExitCode, mirroring what
// cmd/conduit/cli itself reads) and the rendered output — proving the
// decorator/exit-code wiring described in cecdysis.CommandWithResultExitCode
// actually behaves as designed for a real multi-result command, not just
// the throwaway test command in cecdysis's own tests.
package doctor_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/root/doctor"
	"github.com/conduitio/conduit/cmd/conduit/root/doctor/doctorcheck"
	"github.com/conduitio/ecdysis"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
	"github.com/spf13/cobra"
)

// buildDoctorCmd wires a fresh DoctorCommand through the same decorator
// cmd/conduit/cli uses for every CommandWithResult command, standalone
// (not under the full root tree — root.go's own wiring is a one-line
// registration, not doctor-specific behavior).
func buildDoctorCmd(t *testing.T) *cobra.Command {
	t.Helper()
	e := ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
	return e.MustBuildCobraCommand(&doctor.DoctorCommand{})
}

// runDoctor runs cmd with args and returns stdout+stderr combined, the leaf
// *cobra.Command (for ResultExitCode), and any hard-failure error.
func runDoctor(t *testing.T, args ...string) (output string, leaf *cobra.Command, err error) {
	t.Helper()
	cmd := buildDoctorCmd(t)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)

	leaf, err = cmd.ExecuteC()
	return out.String(), leaf, err
}

// tempConfigArgs creates a fresh, isolated temp directory (with the
// "pipelines" subdirectory Config.Validate() requires — see
// doctorcheck_test.go's validConfig for why) and returns the flags needed
// to point a doctor run at it instead of the developer's real
// conduit.yaml / conduit.db / bound ports.
func tempConfigArgs(t *testing.T) (args []string, dir string) {
	t.Helper()
	dir = t.TempDir()
	is.New(t).NoErr(os.MkdirAll(filepath.Join(dir, "pipelines"), 0o755))
	return []string{
		"--config.path=" + filepath.Join(dir, "conduit.yaml"),
		"--db.badger.path=" + filepath.Join(dir, "conduit.db"),
		"--pipelines.path=" + filepath.Join(dir, "pipelines"),
		"--connectors.path=" + filepath.Join(dir, "connectors"),
		"--processors.path=" + filepath.Join(dir, "processors"),
		"--api.grpc.address=127.0.0.1:0",
		"--api.http.address=127.0.0.1:0",
	}, dir
}

// TestDoctor_AllGood_JSON_ExitCode0 exercises the full pipeline end to end:
// a valid, isolated config should produce ok:true, error:null, and exit 0.
func TestDoctor_AllGood_JSON_ExitCode0(t *testing.T) {
	is := is.New(t)
	args, _ := tempConfigArgs(t)
	args = append(args, "--json")

	out, leaf, err := runDoctor(t, args...)
	is.NoErr(err)

	code, hasCode := cecdysis.ResultExitCode(leaf)
	if hasCode {
		is.Equal(code, 0) // an explicit annotation, if any, must be OK
	}

	var got cecdysis.Result
	is.NoErr(json.Unmarshal([]byte(out), &got))
	is.Equal(got.Command, "doctor")
	is.True(got.OK)
	is.True(got.Error == nil)
}

// TestDoctor_InvalidLogLevel_JSON_ExitCode2 is the doctor-level version of
// acceptance criterion 5, driven through the real cobra command: an
// invalid config field fails config.validate, and — critically — the
// domain finding still yields exit code 2 despite ExecuteWithResult
// returning a nil error (see cecdysis.CommandWithResultExitCode's doc for
// why this needs its own test, not just a check-level one).
func TestDoctor_InvalidLogLevel_JSON_ExitCode2(t *testing.T) {
	is := is.New(t)
	args, _ := tempConfigArgs(t)
	args = append(args, "--json", "--log.level=bogus", "--check", doctorcheck.NameConfigValidate)

	out, leaf, err := runDoctor(t, args...)
	is.NoErr(err) // a domain finding is not a HARD failure

	code, ok := cecdysis.ResultExitCode(leaf)
	is.True(ok)
	is.Equal(code, 2)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal([]byte(out), &got))
	is.Equal(got.Command, "doctor")
	is.True(!got.OK)
	is.True(got.Error == nil) // domain finding, not a HARD failure — envelope error stays null

	result, ok := got.Result.(map[string]any)
	is.True(ok)
	checks, ok := result["checks"].([]any)
	is.True(ok)
	is.Equal(len(checks), 1) // --check filtered to exactly one
	first, ok := checks[0].(map[string]any)
	is.True(ok)
	is.Equal(first["status"], "fail")
	is.Equal(first["configPath"], "log.level")
}

// TestDoctor_UnknownCheckName_HardFailure_Exit2 is acceptance criterion 8:
// an unknown --check name is a HARD command failure (envelope error set),
// exit 2, listing valid names.
func TestDoctor_UnknownCheckName_HardFailure_Exit2(t *testing.T) {
	is := is.New(t)
	args, _ := tempConfigArgs(t)
	args = append(args, "--json", "--check", "bogus.check")

	out, _, err := runDoctor(t, args...)
	is.True(err != nil)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal([]byte(out), &got))
	is.True(!got.OK)
	is.True(got.Error != nil)
	is.Equal(got.Error.Code, "common.invalid_argument")
	is.True(len(got.Error.Suggestion) > 0) // lists valid names
}

// TestDoctor_StandaloneCompatWithoutDeep_HardFailure asserts selecting the
// --deep-gated check without --deep is rejected up front with a clear
// message, rather than silently running zero checks.
func TestDoctor_StandaloneCompatWithoutDeep_HardFailure(t *testing.T) {
	is := is.New(t)
	args, _ := tempConfigArgs(t)
	args = append(args, "--check", doctorcheck.NamePluginsStandaloneCompat)

	_, _, err := runDoctor(t, args...)
	is.True(err != nil)
}

// TestDoctor_CheckFilter_RunsExactlyOne asserts --check narrows the run to
// only the named check (acceptance criterion 8, the filtering half).
func TestDoctor_CheckFilter_RunsExactlyOne(t *testing.T) {
	is := is.New(t)
	args, _ := tempConfigArgs(t)
	args = append(args, "--json", "--check", doctorcheck.NamePluginsBuiltin)

	out, _, err := runDoctor(t, args...)
	is.NoErr(err)

	var got cecdysis.Result
	is.NoErr(json.Unmarshal([]byte(out), &got))
	result, ok := got.Result.(map[string]any)
	is.True(ok)
	checks, ok := result["checks"].([]any)
	is.True(ok)
	is.Equal(len(checks), 1)
	first, ok := checks[0].(map[string]any)
	is.True(ok)
	is.Equal(first["name"], doctorcheck.NamePluginsBuiltin)
}

// TestDoctor_Quiet_SuppressesPassButNotWarn exercises the human-readable
// render path with --quiet: a passing check's line disappears, a warning's
// does not.
func TestDoctor_Quiet_SuppressesPassButNotWarn(t *testing.T) {
	is := is.New(t)
	args, _ := tempConfigArgs(t)
	args = append(args, "--quiet", "--check", doctorcheck.NamePluginsBuiltin, "--check", doctorcheck.NamePluginsConnectorsDir)

	out, _, err := runDoctor(t, args...)
	is.NoErr(err)

	// plugins.builtin passes and must be suppressed by --quiet...
	is.True(!strings.Contains(out, doctorcheck.NamePluginsBuiltin))
	// ...but plugins.connectors_dir (missing dir, a Warn) must still show.
	is.True(strings.Contains(out, doctorcheck.NamePluginsConnectorsDir))
}

// TestDoctor_NoDBDirLeftBehind_EndToEnd is acceptance criterion 11 (no side
// effects) driven through the real command, not just doctorcheck directly.
func TestDoctor_NoDBDirLeftBehind_EndToEnd(t *testing.T) {
	is := is.New(t)
	args, dir := tempConfigArgs(t)
	dbPath := filepath.Join(dir, "fresh-subdir", "conduit.db")

	_, statErr := os.Stat(dbPath)
	is.True(os.IsNotExist(statErr))

	// Override the --db.badger.path tempConfigArgs already set, to point at
	// the not-yet-existing path this test actually checks.
	args = append(args, "--db.badger.path="+dbPath, "--check", doctorcheck.NameStoreReachable)
	_, _, err := runDoctor(t, args...)
	is.NoErr(err)

	_, statErr = os.Stat(dbPath)
	is.True(os.IsNotExist(statErr)) // still doesn't exist after the run
}
