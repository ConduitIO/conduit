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

package doctorcheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/client"
	"github.com/conduitio/conduit-connector-protocol/pconnutils"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/plugin/connector/standalone"
	"github.com/rs/zerolog"
)

// PluginSpecCheckArg is the hidden, undocumented first argument doctor's
// standaloneCompatCheck re-execs the running conduit binary with, to run
// CheckConnectorPluginSpec in an isolated child process. It's exported so
// cmd/conduit/root/doctor's hidden worker command can match on it without
// this package (or that one) hardcoding the string twice.
//
// This is not a public CLI surface: `conduit <PluginSpecCheckArg> <path>`
// exists purely as the isolation boundary standaloneCompatCheck's doc
// explains, not as something a user is meant to run directly.
const PluginSpecCheckArg = "__plugin-spec-check"

// pluginSpecCheckTimeout bounds each plugin's subprocess dispense+specify
// round trip. A well-behaved plugin binary answers Specify almost
// instantly; this is generous enough to tolerate a slow process start
// (especially the first invocation of a freshly-built binary) without
// letting a hung or malicious plugin stall `doctor --deep` indefinitely.
const pluginSpecCheckTimeout = 15 * time.Second

// standaloneCompatCheck dispenses every standalone connector plugin binary
// found in connectorsDir and calls Specify on it, the same way
// pkg/plugin/connector/standalone.Registry.loadSpecifications does when
// `conduit run` scans the connectors directory at startup. It is the one
// check in this package that executes external code, and it is opt-in
// (Options.Deep) for exactly that reason.
//
// # Why this runs in a subprocess, not in-process with recover()
//
// hashicorp/go-plugin's client.Client spawns background goroutines in the
// CALLING process to manage the plugin subprocess (stdio copying, health
// checking). A panic in one of those goroutines crashes the process that
// created the client — Go cannot recover a panic across a goroutine
// boundary, so pkg/conduit/check.Run's per-check recover() (which only
// covers a panic happening synchronously inside Check.Run itself) cannot
// protect `conduit doctor` from a broken or malicious plugin binary that
// triggers this. The ADR (20260707-check-engine-shared-by-doctor-and-scaffold.md)
// calls this out explicitly and requires isolating the dispense+specify
// call in its own OS process instead.
//
// So this check does not call CheckConnectorPluginSpec directly: for each
// candidate binary, it re-execs the running conduit binary as
// `<executable> __plugin-spec-check <path>` (see PluginSpecCheckArg) with a
// bounded timeout, and treats any non-clean exit — nonzero exit code, a
// timeout kill, a crash/signal — as a Fail for that plugin. Whatever
// happens inside the child (including a goroutine panic in go-plugin) stays
// contained to that child process; `doctor` itself never links against the
// part of go-plugin that spawns those goroutines directly.
type standaloneCompatCheck struct {
	connectorsDir string
	// executable is the conduit binary to re-exec. DefaultChecks resolves
	// this via os.Executable() when Options.Executable is empty; a test can
	// point it at a small stub binary instead.
	executable string
}

func (c standaloneCompatCheck) Name() string { return NamePluginsStandaloneCompat }

func (c standaloneCompatCheck) Timeout() time.Duration {
	// The batch default (check.DefaultTimeout, 10s) is too short once
	// multiple plugin subprocesses are involved; this check does its own
	// internal per-plugin bounding (pluginSpecCheckTimeout) and needs
	// headroom for several of those in sequence.
	return 2 * time.Minute
}

func (c standaloneCompatCheck) Run(ctx context.Context) check.CheckResult {
	entries, err := os.ReadDir(c.connectorsDir)
	if err != nil {
		// Folded into Warn regardless of the specific error (missing dir,
		// permission problem, or a file where a directory was expected):
		// plugins.connectors_dir already reports directory-shaped problems
		// as their own check result. Duplicating that distinction here
		// under --deep would just be a second, confusingly-differently-worded
		// signal for the same root cause.
		return check.CheckResult{
			Status:   check.StatusWarn,
			Category: CategoryPlugins,
			Message:  fmt.Sprintf("no standalone connector plugins to check in %s: %v", c.connectorsDir, err),
		}
	}

	var candidates []string
	for _, e := range entries {
		if !e.IsDir() {
			candidates = append(candidates, filepath.Join(c.connectorsDir, e.Name()))
		}
	}

	if len(candidates) == 0 {
		return check.CheckResult{
			Status:   check.StatusWarn,
			Category: CategoryPlugins,
			Message:  fmt.Sprintf("%s has no plugin binaries to check", c.connectorsDir),
		}
	}

	if c.executable == "" {
		return check.CheckResult{
			Status:   check.StatusFail,
			Category: CategoryPlugins,
			Code:     conduiterr.CodeInternal.Reason(),
			Message:  "could not determine the path to the running conduit binary, so standalone plugin checks cannot be isolated in a subprocess",
		}
	}

	var failed []string
	for _, path := range candidates {
		if err := c.checkOne(ctx, path); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", filepath.Base(path), err))
		}
	}

	if len(failed) > 0 {
		return check.CheckResult{
			Status:   check.StatusFail,
			Category: CategoryPlugins,
			// Runtime (exit bucket 1), per the design doc's table: an
			// incompatible plugin binary is a plugin/build problem, not an
			// environment-of-execution one (contrast store.reachable /
			// network.*, which are Unavailable).
			Code:       conduiterr.CodeInternal.Reason(),
			ConfigPath: configKeyConnectorsPath,
			Message:    fmt.Sprintf("%d of %d standalone connector plugin(s) failed a compatibility check: %s", len(failed), len(candidates), strings.Join(failed, "; ")),
			Suggestion: "rebuild the failing plugin(s) against the current conduit-connector-protocol version, or remove them from connectors.path",
		}
	}

	return check.CheckResult{
		Status:     check.StatusPass,
		Category:   CategoryPlugins,
		ConfigPath: configKeyConnectorsPath,
		Message:    fmt.Sprintf("%d standalone connector plugin(s) produced a valid specification", len(candidates)),
	}
}

// checkOne runs one plugin's compatibility check in an isolated subprocess
// (see standaloneCompatCheck's doc) and reports any non-clean exit as an
// error, regardless of cause — a nonzero exit, a context-deadline kill, or
// an abnormal termination (crash/signal) from a panic inside the child are
// all treated identically as "this plugin failed the check": Run does not
// try to distinguish them, since all three mean the same thing to a doctor
// user (this plugin binary is not safe/compatible to load).
func (c standaloneCompatCheck) checkOne(ctx context.Context, pluginPath string) error {
	checkCtx, cancel := context.WithTimeout(ctx, pluginSpecCheckTimeout)
	defer cancel()

	//nolint:gosec // c.executable is the running conduit binary's own resolved path, not untrusted input.
	cmd := exec.CommandContext(checkCtx, c.executable, PluginSpecCheckArg, pluginPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if cerrors.Is(checkCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("timed out after %s", pluginSpecCheckTimeout)
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s (%w)", msg, err)
	}
	return nil
}

// CheckConnectorPluginSpec dispenses pluginPath's specifier plugin and
// calls Specify, bounded by ctx. This is the pure worker
// standaloneCompatCheck's subprocess isolation runs — see that type's doc
// for why it must run in its own OS process rather than being called
// directly from the check itself. It mirrors
// pkg/plugin/connector/standalone.Registry.loadSpecifications's dispense
// call shape exactly (same env vars) for behavioral fidelity with what
// `conduit run` actually does when it scans connectors.path.
func CheckConnectorPluginSpec(ctx context.Context, pluginPath string) error {
	dispenser, err := standalone.NewDispenser(
		zerolog.Nop(),
		pluginPath,
		client.WithEnvVar(pconnutils.EnvConduitConnectorUtilitiesGRPCTarget, ""),
		client.WithEnvVar(pconnutils.EnvConduitConnectorToken, "irrelevant-token"),
		client.WithEnvVar(pconnector.EnvConduitConnectorID, "doctor-plugin-spec-check"),
	)
	if err != nil {
		return fmt.Errorf("failed to create connector dispenser: %w", err)
	}

	specifier, err := dispenser.DispenseSpecifier()
	if err != nil {
		return fmt.Errorf("failed to dispense connector specifier (tip: check that this is a valid connector plugin binary and that it's executable): %w", err)
	}

	if _, err := specifier.Specify(ctx, pconnector.SpecifierSpecifyRequest{}); err != nil {
		return fmt.Errorf("failed to get connector specification: %w", err)
	}
	return nil
}
