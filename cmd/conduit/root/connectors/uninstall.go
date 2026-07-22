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
	"time"

	"github.com/conduitio/conduit/cmd/conduit/api"
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/config/yaml"
	"github.com/conduitio/conduit/pkg/registry"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithFlags   = (*UninstallCommand)(nil)
	_ ecdysis.CommandWithConfig  = (*UninstallCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*UninstallCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*UninstallCommand)(nil)
	_ cecdysis.CommandWithResult = (*UninstallCommand)(nil)
)

// uninstallInUseCheckTimeout bounds how long UninstallCommand waits to
// determine whether the running engine is reachable before falling back to
// the offline provisioned-config scan (step5 §2 step 2) — short, since this
// is a local health probe, not a real operation.
const uninstallInUseCheckTimeout = 2 * time.Second

// UninstallFlags embeds conduit.Config for the same reason InstallFlags
// does: --connectors.path (and --pipelines.path, for the offline in-use
// fallback scan) must resolve identically to every other command that
// touches these directories.
type UninstallFlags struct {
	conduit.Config

	Force bool `long:"force" usage:"remove the artifact even if a pipeline still references it (the affected pipelines are still named in the result/warning)"`
}

type UninstallArgs struct {
	Name    string
	Version string // "" = auto-resolve if exactly one version is installed
}

// UninstallCommand implements `conduit connectors uninstall <name>[@version]`.
// Like InstallCommand, this is an OFFLINE command in the sense that it never
// requires the engine to be running — it drives pkg/registry.Uninstall
// directly against --connectors.path on disk. It DOES opportunistically try
// to reach a running engine (a short health-checked dial, never a hard
// requirement) to build the in-use check's pipeline-connector-instance
// list; if the engine isn't reachable, it falls back to scanning
// provisioned pipeline configs directly off disk (pkg/provisioning/config),
// since the in-use check only needs to know what pipelines WOULD run, not
// what is currently running.
type UninstallCommand struct {
	flags UninstallFlags
	args  UninstallArgs
	Cfg   conduit.Config
}

func (c *UninstallCommand) Usage() string { return "uninstall <name>[@version]" }

func (c *UninstallCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Remove an installed standalone connector",
		Long: `uninstall removes a connector artifact and its install-manifest entry. If more
than one version of <name> is installed, an explicit @version is required — an ambiguous
"uninstall <name>" refuses rather than guessing which one you meant.

Before removing anything, uninstall checks whether any pipeline (running, or merely
provisioned on disk) references the exact name@version being removed. By default this
refuses with a list of the affected pipelines; --force proceeds anyway and the result
carries a warning naming them.

Exit codes (via the ConduitError's registered category):
  0  success
  1  Runtime    — internal bug
  2  Validation — not installed, ambiguous uninstall (multiple versions, no @version given)
  3  Environment — connector is in use by a pipeline and --force was not given`,
		Example: "conduit connectors uninstall postgres\n" +
			"conduit connectors uninstall postgres@0.14.1\n" +
			"conduit connectors uninstall postgres --force\n" +
			"conduit connectors uninstall postgres --json",
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
	flags.SetDefault("connectors.path", c.Cfg.Connectors.Path)
	flags.SetDefault("pipelines.path", c.Cfg.Pipelines.Path)
	flags.SetDefault("api.grpc.address", c.Cfg.API.GRPC.Address)

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

// Args parses "<name>[@version]" — same convention as InstallCommand.Args.
func (c *UninstallCommand) Args(args []string) error {
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

func (c *UninstallCommand) ResultCommand() string { return "connectors.uninstall" }

func (c *UninstallCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	refs, err := collectInUseRefs(ctx, c.Cfg, c.args.Name, c.args.Version)
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	result, err := registry.Uninstall(registry.UninstallOptions{
		Name: c.args.Name, Version: c.args.Version, ConnectorsPath: c.Cfg.Connectors.Path,
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

// collectInUseRefs builds the in-use check's pipeline-connector-instance
// list (step5 §2 step 2): if the exact name@version to uninstall is unknown
// yet (Version == "", ambiguous or not, decided later by registry.Uninstall
// itself), this still needs SOME concrete version to match against — an
// empty Version here means "match any installed version of Name" is not
// quite right either, since the manifest may have more than one. To keep
// this simple and correct, the identity match below is by NAME only when
// Version is empty (over-matching is safe here: it can only ADD warnings/
// refusals, never silently miss one), and by the exact name@version
// otherwise.
func collectInUseRefs(ctx context.Context, cfg conduit.Config, name, version string) ([]registry.InUseRef, error) {
	instances, err := listConnectorInstances(ctx, cfg)
	if err != nil {
		return nil, err
	}

	var refs []registry.InUseRef
	for _, inst := range instances {
		fn := plugin.FullName(inst.Plugin)
		if fn.PluginType() != plugin.PluginTypeStandalone || fn.PluginName() != name {
			continue
		}
		if version != "" && fn.PluginVersion() != version {
			continue
		}
		refs = append(refs, registry.InUseRef{PipelineID: inst.PipelineID, ConnectorID: inst.ID})
	}
	return refs, nil
}

// connectorInstanceRef is the minimal shape collectInUseRefs needs from
// either source (the live API or a provisioned-config scan).
type connectorInstanceRef struct {
	ID         string
	PipelineID string
	Plugin     string
}

// listConnectorInstances tries the running engine first (a short
// health-checked dial — see uninstallInUseCheckTimeout) via the SAME
// ConnectorServiceClient.ListConnectors call `connectors list` already
// makes, with no PipelineId filter; if the engine isn't reachable, it falls
// back to scanning provisioned pipeline configs directly off disk via
// pkg/provisioning/config, since uninstall only touches files under
// --connectors.path and must work whether or not the engine is running.
func listConnectorInstances(ctx context.Context, cfg conduit.Config) ([]connectorInstanceRef, error) {
	dialCtx, cancel := context.WithTimeout(ctx, uninstallInUseCheckTimeout)
	defer cancel()

	client, err := api.NewClient(dialCtx, cfg.API.GRPC.Address)
	if err == nil {
		defer client.Close()
		return listConnectorInstancesViaAPI(ctx, client)
	}

	return listConnectorInstancesFromProvisionedConfig(ctx, cfg.Pipelines.Path)
}

func listConnectorInstancesViaAPI(ctx context.Context, client *api.Client) ([]connectorInstanceRef, error) {
	resp, err := client.ConnectorServiceClient.ListConnectors(ctx, &apiv1.ListConnectorsRequest{})
	if err != nil {
		return nil, cerrors.Errorf("failed to list connectors from the running engine: %w", err)
	}
	refs := make([]connectorInstanceRef, 0, len(resp.Connectors))
	for _, c := range resp.Connectors {
		refs = append(refs, connectorInstanceRef{ID: c.Id, PipelineID: c.PipelineId, Plugin: c.Plugin})
	}
	return refs, nil
}

// listConnectorInstancesFromProvisionedConfig reads every provisioned
// pipeline config under path (a file or a directory of them — see
// config.ResolveFiles, the exact same resolution `pipelines validate`/
// config-file provisioning at startup use) and returns every connector
// entry's plugin reference. A missing/empty path is not an error: it
// matches "no provisioned pipelines to check," not "in use."
func listConnectorInstancesFromProvisionedConfig(ctx context.Context, path string) ([]connectorInstanceRef, error) {
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, cerrors.Errorf("could not stat pipelines path %q: %w", path, err)
	}

	files, err := config.ResolveFiles(path)
	if err != nil {
		return nil, cerrors.Errorf("could not resolve pipeline config files under %q: %w", path, err)
	}

	parser := yaml.NewParser(log.Nop())
	var refs []connectorInstanceRef
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			return nil, cerrors.Errorf("could not open pipeline config %q: %w", file, err)
		}
		pipelines, perr := parser.Parse(ctx, f)
		_ = f.Close()
		if perr != nil {
			// A malformed provisioned config is a real problem, but not one
			// uninstall should fail over silently — surface it as a hard
			// error so the operator fixes the config rather than getting a
			// false "not in use" answer.
			return nil, cerrors.Errorf("could not parse pipeline config %q: %w", file, perr)
		}
		for _, p := range pipelines {
			for _, c := range p.Connectors {
				refs = append(refs, connectorInstanceRef{ID: c.ID, PipelineID: p.ID, Plugin: c.Plugin})
			}
		}
	}
	return refs, nil
}
