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

package connectors

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/ecdysis"
)

// defaultTrustAnchors (this build's compiled-in registry root/freshness public
// keys) and anchorLoadErr now live in anchors.go, populated from the embedded
// PEMs at init. Tests override the anchors via SetDefaultTrustAnchorsForTest
// (export_test.go).

// unsignedInstallEnvVar is the non-interactive escape hatch for
// --allow-unsigned (plan-v2 §5/§6): BOTH this env var AND the flag must be
// set together in a non-interactive context (no TTY, CI=true, or
// MCP-originated) for an unsigned install to proceed without a prompt —
// deliberately two independent signals so neither an agent's tool call
// alone nor a copy-pasted script line alone can trigger it.
const unsignedInstallEnvVar = "CONDUIT_ALLOW_UNSIGNED_INSTALL"

var (
	_ ecdysis.CommandWithFlags   = (*InstallCommand)(nil)
	_ ecdysis.CommandWithConfig  = (*InstallCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*InstallCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*InstallCommand)(nil)
	_ cecdysis.CommandWithResult = (*InstallCommand)(nil)
)

// InstallFlags embeds conduit.Config so `conduit connectors install`
// resolves --connectors.path the same way (flags, CONDUIT_ env vars, and
// the config file) as `conduit run`/`conduit doctor` — install writes into
// that same directory, so it must agree with the rest of the CLI on where
// it is. The remaining fields are install-specific.
type InstallFlags struct {
	conduit.Config

	IndexURL    string        `long:"index-url" usage:"registry index URL"`
	IndexFile   string        `long:"index-file" usage:"read the index from a local file instead of --index-url (offline mode)"`
	LockTimeout time.Duration `long:"lock-timeout" usage:"max time to wait to acquire the per-connector install lock"`
	DryRun      bool          `long:"dry-run" usage:"resolve and select a platform artifact; report what would be installed without downloading or writing anything"`

	// Bundle installs fully offline from a tarball produced by `conduit
	// connectors bundle` (PR-4, plan-v2/step5 §7 Phase B) — NO network call
	// of any kind is made when this is set; everything is re-verified from
	// the bundle's own contents. When set, <name>[@version] positional args
	// are ignored (the bundle already names its own connector/version).
	Bundle string `long:"bundle" usage:"install fully offline from a bundle tarball produced by 'conduit connectors bundle' (ignores the positional <name>[@version] argument)"`
	// AllowStaleBundle tolerates a bundled index snapshot older than
	// --max-staleness — gated identically to --allow-unsigned (plan-v2/
	// step5 §7 step 3, ratified by DeVaris): requires interactive
	// confirmation or the non-interactive env var escape hatch, and may be
	// hard-disabled entirely by operator policy (install.allow-stale-bundle).
	// Only ever consulted with --bundle.
	AllowStaleBundle bool `long:"allow-stale-bundle" usage:"tolerate a --bundle whose snapshot is older than --max-staleness — requires interactive confirmation or an explicit non-interactive escape hatch; may be disabled entirely by operator policy (install.allow-stale-bundle)"`

	// AllowUnsigned requests skipping signature/provenance verification —
	// the sha256 corruption check always still runs regardless. Has NO
	// default value that could be silently true, and is not itself
	// sufficient to skip verification: it is gated by policy.Decide via
	// registry.Install (interactive typed confirmation, the
	// CONDUIT_ALLOW_UNSIGNED_INSTALL env var in non-interactive contexts,
	// and operator policy — see install.allow-unsigned in the Conduit
	// config, and plan-v2 §5/§6 for the full behavioral matrix).
	AllowUnsigned bool `long:"allow-unsigned" usage:"install without cryptographic signature/provenance verification — requires interactive confirmation or an explicit non-interactive escape hatch; may be disabled entirely by operator policy (install.allow-unsigned)"`
}

// InstallArgs holds InstallCommand's parsed positional argument.
type InstallArgs struct {
	Name    string
	Version string // "" = newest compatible
}

// InstallCommand implements `conduit connectors install <name>[@version]`.
// It is an OFFLINE command (mirrors doctor/repair, not list/describe): it
// does not dial the running Conduit API — it drives
// pkg/registry.Install directly against --connectors.path on disk.
//
// # The trust core (PR-2)
//
// This is the one production call site for pkg/registry.Install. It passes
// registry.TrustedVerifier for BOTH IndexVerifier and ArtifactVerifier —
// every real invocation of this command now verifies the index signature
// and the artifact's signature + SLSA provenance against the connector's
// pinned identity before installing anything. RequireProvenance is set to
// true here (Tier-1 posture decision): a validly-signed artifact with NO
// SLSA provenance attestation is refused, not installed — "verified" means
// signature AND provenance, never signature alone. defaultTrustAnchors (this
// file) is EMPTY as of this PR — the bootstrap ceremony that generates and
// embeds real Conduit registry root/freshness keys (plan-v2 §9) is separate
// infrastructure work, out of this PR's scope — so every real index this
// build fetches fails closed with registry.CodeTrustAnchorExpired
// ("registry.trust_anchor_expired") until that ceremony lands. This is the
// correct, intentional, fail-closed state of a build that predates the
// ceremony, not a bug and not a silent bypass: there is no flag, no config
// value, and no environment variable that lets an index verify without a
// recognized signature. The one deliberate, heavily-gated exception is
// --allow-unsigned, which skips ONLY the per-artifact signature/provenance
// check (never the sha256 corruption check, never index verification) and
// is itself refused unless policy.Decide's full gate — interactive typed
// confirmation, or an explicit non-interactive env var, and never at all if
// the operator's install.allow-unsigned config is false — allows it.
type InstallCommand struct {
	flags InstallFlags
	args  InstallArgs
	Cfg   conduit.Config
}

func (c *InstallCommand) Usage() string { return "install <name>[@version]" }

func (c *InstallCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Install a standalone connector from the registry index",
		Long: `install resolves <name>[@version] against the registry index (exact-match name lookup,
newest-compatible version selection when @version is omitted), downloads the artifact for this
host's platform, checks its integrity, and — pending the trust core landing in a future
release — refuses at the verification step: this build has no code path that installs an
unverified connector artifact.

Exit codes (via the ConduitError's registered category):
  0  success
  1  Runtime    — internal bug, archive-shape violation, verification not yet available
  2  Validation — connector/version not found, incompatible version, yanked/revoked, no platform artifact
  3  Environment — index unreachable, download failed, corrupt download, install lock contended`,
		Example: "conduit connectors install postgres\n" +
			"conduit connectors install postgres@0.14.1\n" +
			"conduit connectors install postgres --dry-run\n" +
			"conduit connectors install postgres --json",
	}
}

func (c *InstallCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}
	// Assigning c.Cfg here (not just building a local default), mirroring
	// doctor.DoctorCommand.Flags(): ParseConfig's viper.Unmarshal only
	// overwrites fields it has flag/env/file data for, so c.Cfg must start
	// non-zero for --connectors.path's default to survive into
	// ExecuteWithResult.
	c.Cfg = conduit.DefaultConfigWithBasePath(currentPath)
	flags.SetDefault("config.path", c.Cfg.ConduitCfg.Path)
	flags.SetDefault("connectors.path", c.Cfg.Connectors.Path)
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

// Args parses "<name>[@version]" — split on the FIRST "@" only, so a
// version constraint is never mistaken for part of the name. With --bundle
// set, the positional argument is optional and ignored if given (the
// bundle already names its own connector/version) — but Args runs before
// Flags parsing order is guaranteed relative to it in every ecdysis
// command, so the actual "was --bundle set" check happens defensively in
// ExecuteWithResult instead of here; this method only accepts the
// zero-or-one-argument shape unconditionally.
func (c *InstallCommand) Args(args []string) error {
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	if len(args) == 0 {
		return nil // valid for --bundle; ExecuteWithResult rejects it otherwise
	}

	name, version, _ := strings.Cut(args[0], "@")
	if name == "" {
		return cerrors.Errorf("connector name must not be empty")
	}
	c.args.Name = name
	c.args.Version = version
	return nil
}

func (c *InstallCommand) ResultCommand() string { return "connectors.install" }

func (c *InstallCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	if err := guardTrustAnchors(); err != nil {
		return cecdysis.Outcome{}, err
	}
	verifier := &registry.TrustedVerifier{
		Anchors:      defaultTrustAnchors,
		StatePath:    registry.IndexStatePath(c.Cfg.Connectors.Path),
		MaxStaleness: c.Cfg.Install.MaxStaleness,
		// RequireProvenance: true (Tier-1 posture decision) — a "verified"
		// artifact always includes the L3 SLSA build attestation; a
		// validly-signed artifact with no provenance now refuses with
		// trust.CodeProvenanceInvalid rather than installing. See
		// TrustedVerifier.RequireProvenance's doc comment.
		RequireProvenance: true,
	}

	if c.flags.Bundle != "" {
		return c.executeFromBundle(ctx, verifier)
	}
	if c.args.Name == "" {
		return cecdysis.Outcome{}, cerrors.Errorf("requires a connector name, optionally with @version (e.g. postgres or postgres@0.14.1), or --bundle <path>")
	}

	tty := isInteractiveTTY()
	ciEnv := os.Getenv("CI") != ""
	envVarSet := os.Getenv(unsignedInstallEnvVar) == "I_UNDERSTAND"

	// The typed re-confirmation prompt (plan-v2 §5): collected HERE, at the
	// CLI layer, before anything is passed to registry.Install — Install
	// (pkg/registry/install.go) is the only call site of policy.Decide
	// (enforced by the PolicyBypass depguard rule in .golangci.yml) and
	// consumes this as an already-validated bool, never re-implementing the
	// prompt/comparison itself. Only ever attempted when a real interactive
	// terminal is present and --allow-unsigned was actually requested — an
	// MCP-originated or --json/non-interactive invocation never reaches
	// this branch (see cecdysis's --json output gating and IsMCP's doc
	// comment on registry.InstallOptions).
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
		ConnectorsPath: c.Cfg.Connectors.Path,

		IndexURL:  c.flags.IndexURL,
		IndexFile: c.flags.IndexFile,

		// The real trust-core wiring (PR-2): every install attempt now
		// actually verifies the index signature and the artifact's
		// signature + SLSA provenance against the connector's pinned
		// identity. No flag or config value anywhere in this file can
		// bypass VerifyIndex; ArtifactVerifier is only ever skipped via the
		// AllowUnsigned+policy fields below, which still route through
		// registry.Install's own policy.Decide gate.
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
		IsMCP:                 false, // this is the CLI, never the MCP tool — see tools_registry.go once it exists (not yet built, flagged in this PR's description)
		EnvVarSet:             envVarSet,
		TypedConfirmation:     typedConfirmation,
		OperatorAllowUnsigned: c.Cfg.Install.AllowUnsigned,
	}

	result, err := registry.Install(ctx, opts)
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	return cecdysis.Outcome{OK: true, Result: result}, nil
}

// executeFromBundle is `conduit connectors install --bundle <path>`
// (plan-v2/step5 §7 Phase B): fully offline, no network call of any kind —
// see pkg/registry/bundle.go's InstallFromBundle for the verification
// details. Shares this file's own TrustedVerifier wiring (verifier) rather
// than constructing a second, divergent one.
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

	result, err := registry.InstallFromBundle(ctx, registry.InstallBundleOptions{
		BundlePath:     c.flags.Bundle,
		ConnectorsPath: c.Cfg.Connectors.Path,
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

// confirmStaleBundle is the typed re-confirmation prompt for
// --allow-stale-bundle, mirroring confirmUnsignedInstall's exact-match
// (never y/N) convention.
func confirmStaleBundle(in *os.File, out *os.File, bundlePath string) (bool, error) {
	fmt.Fprintf(out, "Type the bundle path (%s) to confirm installing a STALE offline bundle — its index "+
		"snapshot is older than the configured maximum staleness: ", bundlePath)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return false, scanner.Err()
	}
	return strings.TrimSpace(scanner.Text()) == bundlePath, nil
}

// isInteractiveTTY reports whether BOTH stdin and stdout are attached to a
// real terminal — the confirmation prompt needs to both display a message
// (stdout) and read a typed response (stdin); either being redirected/piped
// means this is not a context a human can interactively confirm in.
func isInteractiveTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// confirmUnsignedInstall prints the typed re-confirmation prompt (plan-v2
// §5: "Type the connector name to confirm installing an UNSIGNED artifact —
// no cryptographic identity was verified") and reads one line from in.
// Returns true only on an EXACT match of name — anything else (including a
// y/N-style answer) is treated as a decline, matching the design's explicit
// "a typed exact match, not y/N" requirement.
func confirmUnsignedInstall(in *os.File, out *os.File, name string) (bool, error) {
	fmt.Fprintf(out, "Type the connector name (%s) to confirm installing an UNSIGNED artifact — "+
		"no cryptographic identity was verified: ", name)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return false, scanner.Err() // nil Err() + no Scan() = EOF, treated as decline below
	}
	return strings.TrimSpace(scanner.Text()) == name, nil
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
	return b.String()
}

// runningProtocolVersion reports the conduit-connector-protocol module
// version this binary was built against, via Go's embedded build info —
// avoiding a hand-maintained constant that could drift from go.mod. Falls
// back to "development" (treated by resolve.go's compatibility check as
// satisfying every minProtocolVersion) when build info isn't available,
// e.g. `go run`.
func runningProtocolVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "development"
	}
	const protocolModule = "github.com/conduitio/conduit-connector-protocol"
	for _, dep := range info.Deps {
		if dep.Path == protocolModule {
			return dep.Version
		}
	}
	return "development"
}

// installedByUser returns the current OS user's username, best-effort, for
// the manifest entry's InstalledBy/audit event's Operator fields. An empty
// string (never an error) if it cannot be determined.
func installedByUser() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}
