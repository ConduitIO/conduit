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
	"path/filepath"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithFlags   = (*BundleCommand)(nil)
	_ ecdysis.CommandWithConfig  = (*BundleCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*BundleCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*BundleCommand)(nil)
	_ cecdysis.CommandWithResult = (*BundleCommand)(nil)
)

// BundleFlags embeds conduit.Config only for --config.path resolution
// (matching every other registry command's flag conventions) — bundle prep
// does not touch --connectors.path at all, since its output is a
// standalone tarball, never an install into that directory.
type BundleFlags struct {
	conduit.Config

	OS        string `long:"os" usage:"target operating system (defaults to this host's)"`
	Arch      string `long:"arch" usage:"target architecture (defaults to this host's)"`
	Output    string `long:"output" usage:"output bundle path (defaults to <name>-<version>-<os>-<arch>.bundle.tar.gz)"`
	IndexURL  string `long:"index-url" usage:"registry index URL"`
	IndexFile string `long:"index-file" usage:"read the index from a local file instead of --index-url"`
}

type BundleArgs struct {
	Name    string
	Version string
}

// BundleCommand implements `conduit connectors bundle <name>[@version]`
// (plan-v2/step5 §7 Phase A): prepares a self-contained offline-install
// tarball on a networked machine, running the SAME full verification a
// normal `install` would, before ever writing the tarball — see
// pkg/registry/bundle.go's package doc for the soundness argument. It
// shares install.go's trust-core wiring (defaultTrustAnchors,
// TrustedVerifier) rather than inventing a second, divergent one.
type BundleCommand struct {
	flags BundleFlags
	args  BundleArgs
	Cfg   conduit.Config
}

func (c *BundleCommand) Usage() string { return "bundle <name>[@version]" }

func (c *BundleCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Prepare an offline install bundle for a connector",
		Long: `bundle resolves <name>[@version] against the registry index (same resolution as
install), downloads and fully verifies the artifact (signature + SLSA provenance against the
connector's pinned identity — the same checks a normal install performs, all while online),
and packages the artifact, its signature/provenance bundles (in cosign --bundle form — embedded
cert chain and Rekor inclusion proof, no live Sigstore query needed later), and the FULL signed
index snapshot into a single tarball.

The bundle is a carrier for an already-verified installation, replayed later on a machine with
no network access at all: "conduit connectors install --bundle <path>" re-verifies everything
from the bundle's own contents — it never trusts the bundle just because it exists.

Exit codes (via the ConduitError's registered category):
  0  success
  2  connector/version not found, incompatible version, yanked/revoked, no platform artifact
  3  index unreachable, download failed, corrupt download`,
		Example: "conduit connectors bundle postgres@0.14.1 --os linux --arch amd64 --output postgres.bundle.tar.gz\n" +
			"conduit connectors bundle postgres --json",
	}
}

func (c *BundleCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}
	c.Cfg = conduit.DefaultConfigWithBasePath(currentPath)
	flags.SetDefault("config.path", c.Cfg.ConduitCfg.Path)
	flags.SetDefault("index-url", registry.DefaultIndexURL)

	return flags
}

func (c *BundleCommand) Config() ecdysis.Config {
	path := filepath.Dir(c.flags.ConduitCfg.Path)
	return ecdysis.Config{
		EnvPrefix:     envPrefix,
		Parsed:        &c.Cfg,
		Path:          c.flags.ConduitCfg.Path,
		DefaultValues: conduit.DefaultConfigWithBasePath(path),
	}
}

func (c *BundleCommand) Args(args []string) error {
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

func (c *BundleCommand) ResultCommand() string { return "connectors.bundle" }

func (c *BundleCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	if err := guardTrustAnchors(); err != nil {
		return cecdysis.Outcome{}, err
	}
	verifier := &registry.TrustedVerifier{
		Anchors:           defaultTrustAnchors,
		StatePath:         registry.IndexStatePath(c.Cfg.Connectors.Path),
		MaxStaleness:      c.Cfg.Install.MaxStaleness,
		RequireProvenance: true,
	}

	output := c.flags.Output
	if output == "" {
		output = fmt.Sprintf("%s-%s-%s-%s.bundle.tar.gz", c.args.Name, orLatest(c.args.Version), orHost(c.flags.OS), orHost(c.flags.Arch))
	}

	result, err := registry.Bundle(ctx, registry.BundleOptions{
		Name: c.args.Name, Version: c.args.Version, OutputPath: output,
		IndexURL: c.flags.IndexURL, IndexFile: c.flags.IndexFile,
		GOOS: c.flags.OS, GOArch: c.flags.Arch,
		IndexVerifier: verifier, ArtifactVerifier: verifier,
		RunningConduitVersion:  conduit.Version(false),
		RunningProtocolVersion: runningProtocolVersion(),
	})
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	return cecdysis.Outcome{OK: true, Result: result}, nil
}

func (c *BundleCommand) Render(outcome cecdysis.Outcome) string {
	res, ok := outcome.Result.(*registry.BundleResult)
	if !ok || res == nil {
		return ""
	}
	return fmt.Sprintf("Bundled %s@%s (%s/%s) into %s\n", res.Name, res.Version, res.OS, res.Arch, res.OutputPath)
}

func orLatest(s string) string {
	if s == "" {
		return "latest"
	}
	return s
}

func orHost(s string) string {
	if s == "" {
		return "host"
	}
	return s
}
