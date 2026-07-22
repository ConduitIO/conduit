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
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
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
// # Fail-closed by construction
//
// This is the one production call site for pkg/registry.Install. It passes
// registry.FailClosedVerifier{} for BOTH IndexVerifier and ArtifactVerifier
// — explicitly and visibly, per plan-v2 §2.2 — so every real invocation of
// this command refuses with registry.CodeVerificationUnavailable
// ("registry.verification_unavailable") until PR-2 wires in the real trust
// core. There is no flag, no config value, and no environment variable that
// changes this in this build.
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
		EnvPrefix:     "CONDUIT",
		Parsed:        &c.Cfg,
		Path:          c.flags.ConduitCfg.Path,
		DefaultValues: conduit.DefaultConfigWithBasePath(path),
	}
}

// Args parses "<name>[@version]" — split on the FIRST "@" only, so a
// version constraint is never mistaken for part of the name.
func (c *InstallCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a connector name, optionally with @version (e.g. postgres or postgres@0.14.1)")
	}
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
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
	opts := registry.InstallOptions{
		Name:           c.args.Name,
		Version:        c.args.Version,
		ConnectorsPath: c.Cfg.Connectors.Path,

		IndexURL:  c.flags.IndexURL,
		IndexFile: c.flags.IndexFile,

		// The one production wiring: BOTH verifiers are FailClosedVerifier
		// until PR-2 lands (see InstallCommand's doc). No flag or config
		// value anywhere in this file can change this.
		IndexVerifier:    registry.FailClosedVerifier{},
		ArtifactVerifier: registry.FailClosedVerifier{},

		RunningConduitVersion:  conduit.Version(false),
		RunningProtocolVersion: runningProtocolVersion(),

		InstalledBy: installedByUser(),
		LockTimeout: c.flags.LockTimeout,
		DryRun:      c.flags.DryRun,
	}

	result, err := registry.Install(ctx, opts)
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
