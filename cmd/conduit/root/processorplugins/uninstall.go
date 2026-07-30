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

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/config/yaml"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithFlags   = (*UninstallCommand)(nil)
	_ ecdysis.CommandWithConfig  = (*UninstallCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*UninstallCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*UninstallCommand)(nil)
	_ cecdysis.CommandWithResult = (*UninstallCommand)(nil)
)

// UninstallFlags embeds conduit.Config for the same reason InstallFlags does:
// --processors.path (and --pipelines.path, for the in-use scan) must resolve
// identically to every other command that touches these directories.
type UninstallFlags struct {
	conduit.Config

	Force bool `long:"force" usage:"remove the artifact even if a pipeline still references it (the affected pipelines are still named in the result/warning)"`
}

type UninstallArgs struct {
	Name    string
	Version string // "" = auto-resolve if exactly one version is installed
}

// UninstallCommand implements `conduit processor-plugins uninstall
// <name>[@version]`. Like InstallCommand it is OFFLINE — it drives
// pkg/registry.UninstallProcessor directly against --processors.path on disk,
// no running engine required.
//
// # In-use check (a distinct code path from the connector uninstall)
//
// Before removing anything, uninstall scans the PROCESSORS blocks of every
// provisioned pipeline config under --pipelines.path — both pipeline-level
// processors and connector-level processors — for a standalone:<name>[@version]
// reference (AC-8). This is deliberately NOT the connector uninstall's
// connectors-block scan, and it is provisioned-config-only: unlike a connector
// instance, a running processor's API parent may be a connector rather than a
// pipeline, so it does not cleanly yield the pipeline identity the in-use
// warning needs; the provisioned config is the complete source of truth for
// "what a `conduit run` would reference". By default an in-use reference
// refuses; --force proceeds and the result names the affected pipelines.
type UninstallCommand struct {
	flags UninstallFlags
	args  UninstallArgs
	Cfg   conduit.Config
}

func (c *UninstallCommand) Usage() string { return "uninstall <name>[@version]" }

func (c *UninstallCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Remove an installed standalone WASM processor",
		Long: `uninstall removes a standalone WASM processor artifact and its install-manifest entry
from --processors.path. If more than one version of <name> is installed, an explicit @version is
required — an ambiguous "uninstall <name>" refuses rather than guessing.

Before removing anything, uninstall scans the processors blocks of every provisioned pipeline
config under --pipelines.path for a standalone:<name> reference. By default this refuses with a
list of the affected pipelines; --force proceeds anyway and the result carries a warning naming
them.

Exit codes (via the ConduitError's registered category):
  0  success
  1  Runtime    — internal bug
  2  Validation — not installed, ambiguous uninstall (multiple versions, no @version given)
  3  Environment — processor is in use by a pipeline and --force was not given`,
		Example: "conduit processor-plugins uninstall ai.embed\n" +
			"conduit processor-plugins uninstall ai.embed@0.1.0\n" +
			"conduit processor-plugins uninstall ai.embed --force\n" +
			"conduit processor-plugins uninstall ai.embed --json",
	}
}

func (c *UninstallCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}
	c.Cfg = conduit.DefaultConfigWithBasePath(currentPath)
	flags.SetDefault("config.path", c.Cfg.ConduitCfg.Path)
	flags.SetDefault("processors.path", c.Cfg.Processors.Path)
	flags.SetDefault("pipelines.path", c.Cfg.Pipelines.Path)

	return flags
}

func (c *UninstallCommand) Config() ecdysis.Config {
	path := filepath.Dir(c.flags.ConduitCfg.Path)
	return ecdysis.Config{
		EnvPrefix:     envPrefix,
		Parsed:        &c.Cfg,
		Path:          c.flags.ConduitCfg.Path,
		DefaultValues: conduit.DefaultConfigWithBasePath(path),
	}
}

// Args parses "<name>[@version]" — same convention as InstallCommand.Args, but
// the name is required (uninstall has no --bundle mode).
func (c *UninstallCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a processor name, optionally with @version (e.g. ai.embed or ai.embed@0.1.0)")
	}
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}

	name, version, _ := strings.Cut(args[0], "@")
	if name == "" {
		return cerrors.Errorf("processor name must not be empty")
	}
	c.args.Name = name
	c.args.Version = version
	return nil
}

func (c *UninstallCommand) ResultCommand() string { return "processor-plugins.uninstall" }

func (c *UninstallCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	refs, err := collectProcessorInUseRefs(ctx, c.Cfg.Pipelines.Path, c.args.Name, c.args.Version)
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	result, err := registry.UninstallProcessor(registry.UninstallProcessorOptions{
		Name: c.args.Name, Version: c.args.Version, ProcessorsPath: c.Cfg.Processors.Path,
		Force: c.flags.Force, InUseRefs: refs, InstalledBy: installedByUser(),
	})
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	return cecdysis.Outcome{OK: true, Result: result}, nil
}

func (c *UninstallCommand) Render(outcome cecdysis.Outcome) string {
	res, ok := outcome.Result.(*registry.UninstallResult)
	if !ok || res == nil {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Uninstalled %s@%s\n", res.Name, res.Version)
	for _, w := range res.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	if res.DriftDetected {
		fmt.Fprintln(&b, "note: the removed artifact's digest did not match the one recorded at install time")
	}
	if res.ArtifactAlreadyMissing {
		fmt.Fprintln(&b, "note: the artifact file was already missing on disk; only the manifest entry was cleaned up")
	}
	return b.String()
}

// collectProcessorInUseRefs scans the processors blocks of every provisioned
// pipeline config under pipelinesPath (both pipeline-level and connector-level
// processors) for a standalone:<name> reference matching the name@version being
// uninstalled — the distinct in-use code path AC-8 requires. A missing/empty
// path is not an error (no provisioned pipelines to check == not in use).
//
// Version matching is deliberately loose: a standalone reference with no
// explicit @version defaults to "latest" (plugin.FullName), so a pipeline
// referencing standalone:ai.embed (unpinned) is treated as in-use for ANY
// installed version. When an explicit @version is being uninstalled, a
// reference is in-use if it is unpinned (latest) OR pins that exact version.
// Over-matching here can only ADD a refusal/warning, never silently miss one.
func collectProcessorInUseRefs(ctx context.Context, pipelinesPath, name, version string) ([]registry.InUseRef, error) {
	if pipelinesPath == "" {
		return nil, nil
	}
	if _, err := os.Stat(pipelinesPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, cerrors.Errorf("could not stat pipelines path %q: %w", pipelinesPath, err)
	}

	files, err := config.ResolveFiles(pipelinesPath)
	if err != nil {
		return nil, cerrors.Errorf("could not resolve pipeline config files under %q: %w", pipelinesPath, err)
	}

	parser := yaml.NewParser(log.Nop())
	var refs []registry.InUseRef
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			return nil, cerrors.Errorf("could not open pipeline config %q: %w", file, err)
		}
		pipelines, perr := parser.Parse(ctx, f)
		_ = f.Close()
		if perr != nil {
			// A malformed provisioned config is a real problem, but not one
			// uninstall should silently ignore into a false "not in use" —
			// surface it so the operator fixes the config.
			return nil, cerrors.Errorf("could not parse pipeline config %q: %w", file, perr)
		}
		for _, p := range pipelines {
			// Pipeline-level processors.
			for _, proc := range p.Processors {
				if procRefMatches(proc.Plugin, name, version) {
					refs = append(refs, registry.InUseRef{PipelineID: p.ID, ConnectorID: proc.ID})
				}
			}
			// Connector-level processors.
			for _, conn := range p.Connectors {
				for _, proc := range conn.Processors {
					if procRefMatches(proc.Plugin, name, version) {
						refs = append(refs, registry.InUseRef{PipelineID: p.ID, ConnectorID: proc.ID})
					}
				}
			}
		}
	}
	return refs, nil
}

// procRefMatches reports whether a processor's plugin reference (e.g.
// "standalone:ai.embed" or "standalone:ai.embed@0.1.0") targets the standalone
// processor name (and version, loosely) being uninstalled. A builtin processor
// reference (no "standalone:" prefix) never matches. See
// collectProcessorInUseRefs for the version-matching rationale.
func procRefMatches(pluginRef, name, version string) bool {
	fn := plugin.FullName(pluginRef)
	if fn.PluginType() != plugin.PluginTypeStandalone || fn.PluginName() != name {
		return false
	}
	if version == "" {
		return true
	}
	refVersion := fn.PluginVersion()
	return refVersion == plugin.PluginVersionLatest || refVersion == version
}
