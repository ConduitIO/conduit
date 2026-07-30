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

package processorplugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/root/connectors"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithFlags   = (*InstallCommand)(nil)
	_ ecdysis.CommandWithConfig  = (*InstallCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*InstallCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*InstallCommand)(nil)
	_ cecdysis.CommandWithResult = (*InstallCommand)(nil)
)

// InstallFlags embeds conduit.Config so `conduit processor-plugins install`
// resolves --processors.path the same way (flags, CONDUIT_ env vars, config
// file) as `conduit run` — install writes into that same directory, so it must
// agree with the rest of the CLI on where it is. The remaining fields are
// install-specific and mirror `conduit connectors install` exactly.
type InstallFlags struct {
	conduit.Config

	IndexURL    string        `long:"index-url" usage:"registry index URL"`
	IndexFile   string        `long:"index-file" usage:"read the index from a local file instead of --index-url (offline mode)"`
	LockTimeout time.Duration `long:"lock-timeout" usage:"max time to wait to acquire the per-processor install lock"`
	DryRun      bool          `long:"dry-run" usage:"resolve and select the arch-neutral WASM artifact; report what would be installed without downloading or writing anything"`

	// Bundle installs fully offline from a signed processor bundle tarball
	// (Tranche A, AC-13) — NO network call of any kind is made; everything is
	// re-verified from the bundle's own contents. When set, the positional
	// <name>[@version] argument is ignored (the bundle names its own
	// processor/version).
	Bundle string `long:"bundle" usage:"install fully offline from a signed processor bundle tarball (ignores the positional <name>[@version] argument)"`
	// AllowStaleBundle tolerates a bundled index snapshot older than
	// --install.max-staleness — gated identically to --allow-unsigned:
	// interactive confirmation or the non-interactive escape-hatch env var, and
	// hard-disablable by operator policy (install.allow-stale-bundle). Only ever
	// consulted with --bundle.
	AllowStaleBundle bool `long:"allow-stale-bundle" usage:"tolerate a --bundle whose snapshot is older than the maximum staleness — requires interactive confirmation or an explicit non-interactive escape hatch; may be disabled entirely by operator policy (install.allow-stale-bundle)"`

	// AllowUnsigned requests skipping signature/provenance verification — the
	// sha256 corruption check always still runs. It is NOT sufficient on its
	// own: it is gated by policy.Decide via registry.InstallProcessor (the SAME
	// single call site the connector path uses, enforced by the PolicyBypass
	// depguard rule) — interactive typed confirmation, the
	// CONDUIT_ALLOW_UNSIGNED_INSTALL env var in non-interactive contexts, and
	// operator policy (install.allow-unsigned).
	AllowUnsigned bool `long:"allow-unsigned" usage:"install without cryptographic signature/provenance verification — requires interactive confirmation or an explicit non-interactive escape hatch; may be disabled entirely by operator policy (install.allow-unsigned)"`
}

// InstallArgs holds InstallCommand's parsed positional argument.
type InstallArgs struct {
	Name    string
	Version string // "" = newest compatible
}

// InstallCommand implements `conduit processor-plugins install <name>[@version]`.
//
// # Offline, deliberately (a divergence from its list/describe siblings)
//
// Unlike `processor-plugins list`/`describe`, which are ONLINE commands that
// query a running engine over gRPC, install is OFFLINE: it never dials the
// engine — it drives pkg/registry.InstallProcessor directly against
// --processors.path on disk, so it works before any engine is running (the
// contract is "install, then `conduit run` discovers it at startup"). This is
// the same offline posture `conduit connectors install` has relative to
// `connectors list`.
//
// # The trust core (reused, never forked)
//
// This passes registry.TrustedVerifier for BOTH IndexVerifier and
// ArtifactVerifier, built from this build's compiled-in ceremony root/freshness
// keys (the SAME single embedded anchor set the connector install uses — see
// anchors.go), with RequireProvenance: true. Signature verification, SLSA
// provenance, identity pinning, and the --allow-unsigned policy.Decide gate all
// run through the one pkg/registry code path; there is no processor-specific
// trust logic.
//
// # Tranche A vs Tranche B
//
// Against the live hosted index (Tranche B), install <name> resolves the
// processor collection. Until the hosted index carries processors (PR-D), that
// path returns registry.processor_not_found with a suggestion pointing at the
// interim offline paths — the Tranche A --index-file / --bundle / gated
// --allow-unsigned flows, which are fully usable today.
type InstallCommand struct {
	flags InstallFlags
	args  InstallArgs
	Cfg   conduit.Config
}

func (c *InstallCommand) Usage() string { return "install <name>[@version]" }

func (c *InstallCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Install a standalone WASM processor from the registry index",
		Long: `install resolves <name>[@version] against the registry index's processor collection
(exact-match name lookup, newest-compatible version selection when @version is omitted),
downloads the single arch-neutral WASM artifact, verifies its signature + SLSA provenance
against the processor's pinned publisher identity, compiles it to confirm it is a loadable
standalone processor whose name matches, and atomically places it under --processors.path so
the next 'conduit run' discovers it.

This is an OFFLINE command: unlike 'processor-plugins list'/'describe' (which query a running
engine), install writes to --processors.path on disk with no engine running.

Interim offline paths (until the hosted index serves processors):
  --index-file <local.json>   install from a locally-provided, still signature-verified index
  --bundle <path.tgz>         install fully offline from a signed processor bundle

Exit codes (via the ConduitError's registered category):
  0  success
  1  Runtime    — internal bug, archive-shape violation, invalid processor artifact
  2  Validation — processor/version not found, incompatible version, yanked/revoked
  3  Environment — index unreachable, download failed, corrupt download, install lock contended`,
		Example: "conduit processor-plugins install ai.embed\n" +
			"conduit processor-plugins install ai.embed@0.1.0\n" +
			"conduit processor-plugins install ai.embed --dry-run\n" +
			"conduit processor-plugins install --index-file ./index.json ai.embed\n" +
			"conduit processor-plugins install --bundle ./ai-embed.tgz\n" +
			"conduit processor-plugins install ai.embed --json",
	}
}

func (c *InstallCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}
	// Assign c.Cfg (not just a local default), mirroring connectors install:
	// ParseConfig's viper.Unmarshal only overwrites fields it has flag/env/file
	// data for, so c.Cfg must start non-zero for --processors.path's default to
	// survive into ExecuteWithResult.
	c.Cfg = conduit.DefaultConfigWithBasePath(currentPath)
	flags.SetDefault("config.path", c.Cfg.ConduitCfg.Path)
	flags.SetDefault("processors.path", c.Cfg.Processors.Path)
	flags.SetDefault("index-url", registry.DefaultIndexURL)
	flags.SetDefault("lock-timeout", registry.DefaultLockTimeout)

	return flags
}

func (c *InstallCommand) Config() ecdysis.Config {
	path := filepath.Dir(c.flags.ConduitCfg.Path)
	return ecdysis.Config{
		EnvPrefix:     envPrefix,
		Parsed:        &c.Cfg,
		Path:          c.flags.ConduitCfg.Path,
		DefaultValues: conduit.DefaultConfigWithBasePath(path),
	}
}

// Args parses "<name>[@version]" — split on the FIRST "@" only. With --bundle
// set, the positional argument is optional and ignored (the bundle names its
// own processor/version); the "was --bundle set" check happens defensively in
// ExecuteWithResult, so this method only accepts the zero-or-one-argument shape.
func (c *InstallCommand) Args(args []string) error {
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	if len(args) == 0 {
		return nil // valid for --bundle; ExecuteWithResult rejects it otherwise
	}

	name, version, _ := strings.Cut(args[0], "@")
	if name == "" {
		return cerrors.Errorf("processor name must not be empty")
	}
	c.args.Name = name
	c.args.Version = version
	return nil
}

func (c *InstallCommand) ResultCommand() string { return "processor-plugins.install" }

func (c *InstallCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	if err := guardTrustAnchors(); err != nil {
		return cecdysis.Outcome{}, err
	}
	verifier := &registry.TrustedVerifier{
		Anchors:      defaultTrustAnchors,
		StatePath:    registry.IndexStatePath(c.Cfg.Processors.Path),
		MaxStaleness: c.Cfg.Install.MaxStaleness,
		// RequireProvenance: true (same Tier-1 posture as connectors) — a
		// "verified" processor artifact always includes the L3 SLSA
		// attestation; a validly-signed artifact with no provenance refuses.
		RequireProvenance: true,
	}

	if c.flags.Bundle != "" {
		return c.executeFromBundle(ctx, verifier)
	}
	if c.args.Name == "" {
		return cecdysis.Outcome{}, cerrors.Errorf("requires a processor name, optionally with @version (e.g. ai.embed or ai.embed@0.1.0), or --bundle <path>")
	}

	tty := isInteractiveTTY()
	ciEnv := os.Getenv("CI") != ""
	envVarSet := os.Getenv(connectors.UnsignedInstallEnvVar) == "I_UNDERSTAND"

	// The typed re-confirmation prompt is collected HERE, at the CLI layer,
	// before anything reaches registry.InstallProcessor — which routes through
	// the SAME single policy.Decide call site the connector path uses (enforced
	// by the PolicyBypass depguard rule) and consumes this as an
	// already-validated bool, never re-implementing the prompt. Only attempted
	// on a real interactive terminal with --allow-unsigned actually requested.
	typedConfirmation := false
	if c.flags.AllowUnsigned && tty && !ciEnv {
		var err error
		typedConfirmation, err = confirmUnsignedInstall(os.Stdin, os.Stdout, c.args.Name)
		if err != nil {
			return cecdysis.Outcome{}, cerrors.Errorf("could not read confirmation: %w", err)
		}
	}

	opts := registry.InstallOptions{
		Name:           c.args.Name,
		Version:        c.args.Version,
		ProcessorsPath: c.Cfg.Processors.Path,

		IndexURL:  c.flags.IndexURL,
		IndexFile: c.flags.IndexFile,

		IndexVerifier:    verifier,
		ArtifactVerifier: verifier,

		RunningConduitVersion:  conduit.Version(false),
		RunningProtocolVersion: runningProtocolVersion(),

		InstalledBy: installedByUser(),
		LockTimeout: c.flags.LockTimeout,
		DryRun:      c.flags.DryRun,

		AllowUnsigned:         c.flags.AllowUnsigned,
		TTY:                   tty,
		CIEnv:                 ciEnv,
		IsMCP:                 false, // this is the CLI, never the MCP tool
		EnvVarSet:             envVarSet,
		TypedConfirmation:     typedConfirmation,
		OperatorAllowUnsigned: c.Cfg.Install.AllowUnsigned,
	}

	result, err := registry.InstallProcessor(ctx, opts)
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	return cecdysis.Outcome{OK: true, Result: result}, nil
}

// executeFromBundle is `conduit processor-plugins install --bundle <path>`
// (Tranche A, AC-13): fully offline, no network call — see
// pkg/registry/bundle.go's InstallProcessorBundle for the verification details.
// Shares this command's own TrustedVerifier wiring rather than constructing a
// second, divergent one.
func (c *InstallCommand) executeFromBundle(ctx context.Context, verifier *registry.TrustedVerifier) (cecdysis.Outcome, error) {
	tty := isInteractiveTTY()
	ciEnv := os.Getenv("CI") != ""
	envVarSet := os.Getenv(registry.StaleBundleEnvVar) == "I_UNDERSTAND"

	typedConfirmation := false
	if c.flags.AllowStaleBundle && tty && !ciEnv {
		var err error
		typedConfirmation, err = confirmStaleBundle(os.Stdin, os.Stdout, c.flags.Bundle)
		if err != nil {
			return cecdysis.Outcome{}, cerrors.Errorf("could not read confirmation: %w", err)
		}
	}

	result, err := registry.InstallProcessorBundle(ctx, registry.InstallBundleOptions{
		BundlePath:     c.flags.Bundle,
		ProcessorsPath: c.Cfg.Processors.Path,
		Verifier:       verifier,
		InstalledBy:    installedByUser(),
		LockTimeout:    c.flags.LockTimeout,

		RunningConduitVersion:  conduit.Version(false),
		RunningProtocolVersion: runningProtocolVersion(),

		AllowStaleBundle:         c.flags.AllowStaleBundle,
		TTY:                      tty,
		CIEnv:                    ciEnv,
		IsMCP:                    false,
		EnvVarSet:                envVarSet,
		TypedConfirmation:        typedConfirmation,
		OperatorAllowStaleBundle: c.Cfg.Install.AllowStaleBundle,
	})
	if err != nil {
		return cecdysis.Outcome{}, err
	}
	return cecdysis.Outcome{OK: true, Result: result}, nil
}

func (c *InstallCommand) Render(outcome cecdysis.Outcome) string {
	res, ok := outcome.Result.(*registry.InstallResult)
	if !ok || res == nil {
		return ""
	}

	var b strings.Builder
	switch {
	case res.DryRun:
		fmt.Fprintf(&b, "Would install %s@%s (%s/%s) from %s\n", res.Name, res.Version, res.OS, res.Arch, res.ArtifactURL)
	case res.AlreadyInstalled:
		fmt.Fprintf(&b, "%s@%s is already installed (%s)\n", res.Name, res.Version, res.ArtifactFile)
	default:
		fmt.Fprintf(&b, "Installed %s@%s (%s/%s) as %s\n", res.Name, res.Version, res.OS, res.Arch, res.ArtifactFile)
	}
	if res.Deprecated {
		fmt.Fprintf(&b, "warning: %s@%s is deprecated\n", res.Name, res.Version)
	}
	// Edge case 16 (egress reminder): a processor that reaches out to a provider
	// (embedding/chunking) will fail closed on the first `conduit run` unless
	// egress is opened — the engine denies all processor egress by default.
	// Actionable-error hygiene: remind on a real install (never on dry-run).
	if !res.DryRun && !res.AlreadyInstalled && looksLikeEgressingProcessor(res.Name) {
		fmt.Fprintf(&b, "note: %s may make outbound provider calls; set processors.egress.* to allow them, or the first run will fail closed\n", res.Name)
	}
	return b.String()
}

// looksLikeEgressingProcessor is a best-effort heuristic for the egress
// reminder (edge case 16) — it never gates the install, only whether an
// informational note is printed. Kept deliberately narrow (embedding-style
// names) to avoid noise on processors that do no I/O.
func looksLikeEgressingProcessor(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "embed") || strings.Contains(n, "rerank")
}
