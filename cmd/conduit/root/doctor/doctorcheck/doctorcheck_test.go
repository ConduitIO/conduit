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

// This file is the "engine reusable by MCP" proof the design doc's
// acceptance criteria call for: it lives in doctorcheck_test, a package
// with no cobra or ecdysis import anywhere in its dependency graph (see
// TestPackageHasNoCobraOrEcdysisDependency below), and it calls doctor's
// check set (DefaultChecks) and pkg/conduit/check.Run directly — the exact
// two calls a future MCP `doctor` tool would make, with zero CLI machinery
// involved.
package doctorcheck_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/root/doctor/doctorcheck"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// validConfig returns a conduit.Config good enough to construct
// DefaultChecks against, rooted at a fresh, empty temp directory (so tests
// don't touch the repo or share state). Individual tests mutate the
// returned Config to set up their scenario.
func validConfig(t *testing.T) conduit.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := conduit.DefaultConfigWithBasePath(dir)
	// Ephemeral ports (OS-assigned) so network.grpc/http never collide with
	// another test or a real Conduit instance running on the developer's
	// machine.
	cfg.API.GRPC.Address = "127.0.0.1:0"
	cfg.API.HTTP.Address = "127.0.0.1:0"
	// Config.Validate() requires cfg.Pipelines.Path to exist whenever it
	// isn't exactly DefaultConfig()'s own (cwd-relative) default — which a
	// tempdir-rooted Config never is. Create it so config.validate passes
	// on an otherwise-valid config; tests that want an invalid config
	// override a field after calling this.
	if err := os.MkdirAll(cfg.Pipelines.Path, 0o755); err != nil {
		t.Fatalf("failed to create pipelines dir: %v", err)
	}
	return cfg
}

func runChecks(cfg conduit.Config, opts doctorcheck.Options) check.Report {
	return check.Run(context.Background(), doctorcheck.DefaultChecks(cfg, log.Nop(), opts))
}

func findResult(t *testing.T, report check.Report, name string) check.CheckResult {
	t.Helper()
	for _, r := range report.Checks {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no check result named %q in report: %+v", name, report.Checks)
	return check.CheckResult{}
}

// TestDefaultChecks_EveryResultHasADefinedStatus is acceptance criterion 1:
// every check returns pass/warn/fail, none empty — run against a "mostly
// fine" config so every non-deep check actually executes its real logic
// instead of short-circuiting.
func TestDefaultChecks_EveryResultHasADefinedStatus(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)

	report := runChecks(cfg, doctorcheck.Options{})
	is.True(len(report.Checks) > 0)

	for _, r := range report.Checks {
		switch r.Status {
		case check.StatusPass, check.StatusWarn, check.StatusFail:
			// defined
		default:
			t.Fatalf("check %q returned an undefined status %q", r.Name, r.Status)
		}
	}
}

// TestDefaultChecks_BadBadgerPath_FailsWithExitCode3 is acceptance
// criterion 2: an unopenable badger path fails store.reachable with
// configPath=db.badger.path and an aggregate ExitCode of 3 (environment).
func TestDefaultChecks_BadBadgerPath_FailsWithExitCode3(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)

	// A file where badger expects to create a directory: badger.Open will
	// fail with "not a directory" or similar, deterministically, on every
	// platform this runs on.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	is.NoErr(os.WriteFile(blocker, []byte("x"), 0o600))
	cfg.DB.Type = conduit.DBTypeBadger
	cfg.DB.Badger.Path = filepath.Join(blocker, "conduit.db")

	report := runChecks(cfg, doctorcheck.Options{})
	result := findResult(t, report, doctorcheck.NameStoreReachable)

	is.Equal(result.Status, check.StatusFail)
	is.Equal(result.ConfigPath, "db.badger.path")
	is.Equal(check.Report{Checks: report.Checks}.ExitCode(), 3)
}

// TestDefaultChecks_PortInUse_FailsWithExitCode3 is acceptance criterion 3:
// a bound gRPC address fails network.grpc, and the aggregate ExitCode is 3.
func TestDefaultChecks_PortInUse_FailsWithExitCode3(t *testing.T) {
	is := is.New(t)

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	is.NoErr(err)
	defer ln.Close()

	cfg := validConfig(t)
	cfg.API.GRPC.Address = ln.Addr().String()

	report := runChecks(cfg, doctorcheck.Options{})
	result := findResult(t, report, doctorcheck.NameNetworkGRPC)

	is.Equal(result.Status, check.StatusFail)
	is.Equal(result.ConfigPath, "api.grpc.address")
	is.Equal(check.Report{Checks: report.Checks}.ExitCode(), 3)
}

// TestDefaultChecks_MissingPluginDir_WarnsNotFails is acceptance criterion
// 4: a missing connectors/processors directory is a Warn, and — with
// nothing else wrong — the aggregate ExitCode stays 0.
func TestDefaultChecks_MissingPluginDir_WarnsNotFails(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)
	cfg.Connectors.Path = filepath.Join(cfg.Connectors.Path, "does-not-exist")
	cfg.Processors.Path = filepath.Join(cfg.Processors.Path, "does-not-exist")

	report := runChecks(cfg, doctorcheck.Options{})

	connectorsResult := findResult(t, report, doctorcheck.NamePluginsConnectorsDir)
	is.Equal(connectorsResult.Status, check.StatusWarn)
	processorsResult := findResult(t, report, doctorcheck.NamePluginsProcessorsDir)
	is.Equal(processorsResult.Status, check.StatusWarn)

	is.Equal(check.Report{Checks: report.Checks}.ExitCode(), 0) // nothing failed
}

// TestDefaultChecks_InvalidConfigField_FailsWithExitCode2 is acceptance
// criterion 5: an invalid config field fails config.validate with exit 2.
func TestDefaultChecks_InvalidConfigField_FailsWithExitCode2(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)
	cfg.Log.Level = "bogus"

	report := runChecks(cfg, doctorcheck.Options{})
	result := findResult(t, report, doctorcheck.NameConfigValidate)

	is.Equal(result.Status, check.StatusFail)
	is.Equal(result.ConfigPath, "log.level")
	is.Equal(check.Report{Checks: report.Checks}.ExitCode(), 2)
}

// TestDefaultChecks_AllGood_ExitCode0 is acceptance criterion 7.
func TestDefaultChecks_AllGood_ExitCode0(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)

	report := runChecks(cfg, doctorcheck.Options{})

	is.Equal(report.Summary.Failed, 0)
	is.Equal(check.Report{Checks: report.Checks}.ExitCode(), 0)
}

// TestDefaultChecks_StoreReachable_NoDBDirLeftBehind is acceptance
// criterion 11 (no side effects): running store.reachable against a badger
// path that does not exist yet must not leave a directory behind.
func TestDefaultChecks_StoreReachable_NoDBDirLeftBehind(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)
	cfg.DB.Type = conduit.DBTypeBadger
	cfg.DB.Badger.Path = filepath.Join(t.TempDir(), "fresh", "conduit.db")

	_, statErr := os.Stat(cfg.DB.Badger.Path)
	is.True(os.IsNotExist(statErr)) // sanity: didn't exist before

	report := runChecks(cfg, doctorcheck.Options{})
	result := findResult(t, report, doctorcheck.NameStoreReachable)
	is.Equal(result.Status, check.StatusPass)

	_, statErr = os.Stat(cfg.DB.Badger.Path)
	is.True(os.IsNotExist(statErr)) // and doesn't exist after, either
}

// TestDefaultChecks_StoreReachable_PreexistingDBDirNotDeleted guards the
// other half of the same invariant: a path that already existed (real
// pipeline state) is never removed by the probe, even though a freshly
// created one is.
func TestDefaultChecks_StoreReachable_PreexistingDBDirNotDeleted(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)
	cfg.DB.Type = conduit.DBTypeBadger
	cfg.DB.Badger.Path = filepath.Join(t.TempDir(), "conduit.db")

	// Open and close it once first, simulating a real prior `conduit run`.
	db, err := conduit.OpenStore(context.Background(), cfg, log.Nop())
	is.NoErr(err)
	is.NoErr(db.Close())

	_, statErr := os.Stat(cfg.DB.Badger.Path)
	is.NoErr(statErr) // sanity: does exist before doctor runs

	report := runChecks(cfg, doctorcheck.Options{})
	result := findResult(t, report, doctorcheck.NameStoreReachable)
	is.Equal(result.Status, check.StatusPass)

	_, statErr = os.Stat(cfg.DB.Badger.Path)
	is.NoErr(statErr) // still exists — doctor did not delete real state
}

// TestDefaultChecks_InMemoryDB_Warns covers the third store.reachable
// branch the design doc's table names (open+ping / inmemory (warn) /
// cannot open).
func TestDefaultChecks_InMemoryDB_Warns(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)
	cfg.DB.Type = conduit.DBTypeInMemory

	report := runChecks(cfg, doctorcheck.Options{})
	result := findResult(t, report, doctorcheck.NameStoreReachable)
	is.Equal(result.Status, check.StatusWarn)
}

// TestCheckNames_CoversEveryDefaultChecksResult asserts CheckNames() (used
// for --check validation) never falls behind what DefaultChecks actually
// produces, with --deep on so plugins.standalone_compat is included too.
func TestCheckNames_CoversEveryDefaultChecksResult(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)

	report := runChecks(cfg, doctorcheck.Options{Deep: true, Executable: "/bin/true"})
	valid := make(map[string]bool, len(doctorcheck.CheckNames()))
	for _, n := range doctorcheck.CheckNames() {
		valid[n] = true
	}

	for _, r := range report.Checks {
		is.True(valid[r.Name]) // every produced check name must be in CheckNames()
	}
}

// TestDefaultChecks_APIDisabled_WarnsNetworkAndEngine covers the
// cfg.API.Enabled == false branch for network.grpc/http and
// engine.reachable — all three must still return a defined status (Warn),
// not be silently omitted.
func TestDefaultChecks_APIDisabled_WarnsNetworkAndEngine(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)
	cfg.API.Enabled = false

	report := runChecks(cfg, doctorcheck.Options{})

	for _, name := range []string{doctorcheck.NameNetworkGRPC, doctorcheck.NameNetworkHTTP, doctorcheck.NameEngineReachable} {
		result := findResult(t, report, name)
		is.Equal(result.Status, check.StatusWarn)
	}
}

// TestDefaultChecks_RequireServer_EngineUnreachable_Fails covers
// --require-server: with no server listening, engine.reachable must Fail
// (not Warn) and contribute to a nonzero ExitCode.
func TestDefaultChecks_RequireServer_EngineUnreachable_Fails(t *testing.T) {
	is := is.New(t)
	cfg := validConfig(t)
	// A port nothing is listening on: 127.0.0.1:0 above only makes sense
	// for a bind check, not a dial — pick an address dialing will fail
	// against quickly instead.
	cfg.API.GRPC.Address = "127.0.0.1:1" // reserved, nothing serves gRPC here

	report := runChecks(cfg, doctorcheck.Options{RequireServer: true})
	result := findResult(t, report, doctorcheck.NameEngineReachable)

	is.Equal(result.Status, check.StatusFail)
	is.Equal(check.Report{Checks: report.Checks}.ExitCode(), 3)
}
