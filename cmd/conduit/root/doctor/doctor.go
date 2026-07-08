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

// Package doctor is the cobra-facing wrapper around doctorcheck's pure
// check set: `conduit doctor`, per
// docs/design-documents/20260707-cli-doctor.md. It answers "would `conduit
// run` succeed here?" as an offline, non-destructive preflight — it does
// not boot a Runtime. This is distinct from the running server's /readyz
// and /healthz endpoints (pkg/conduit/readyz.go), which answer "is the
// running engine serving?".
//
// This package (unlike its doctorcheck subpackage) does import cobra and
// ecdysis — it is the CLI-facing layer the ADR describes ("doctor
// (cmd/conduit/root/doctor + its check set) imports check and supplies its
// runtime checks"). All engine-reusable logic lives in doctorcheck; nothing
// here is needed to compose or run doctor's checks in a non-CLI context.
package doctor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/root/doctor/doctorcheck"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithFlags           = (*DoctorCommand)(nil)
	_ ecdysis.CommandWithConfig          = (*DoctorCommand)(nil)
	_ ecdysis.CommandWithDocs            = (*DoctorCommand)(nil)
	_ cecdysis.CommandWithResult         = (*DoctorCommand)(nil)
	_ cecdysis.CommandWithResultExitCode = (*DoctorCommand)(nil)
)

// DoctorFlags embeds conduit.Config so `conduit doctor` accepts the same
// config-affecting flags (--db.badger.path, --api.grpc.address, and so on)
// as `conduit run` — doctor's job is to check that configuration, so it
// needs to be settable the same way. The remaining fields are doctor-only
// behavioral flags (see the CLI output conventions for --check, --quiet,
// --deep, --require-server; --no-color is this command's own, since no
// other command has registered a global one yet).
type DoctorFlags struct {
	conduit.Config

	Deep          bool     `long:"deep" usage:"also run plugins.standalone_compat, which dispenses standalone connector plugin binaries in an isolated subprocess"`
	RequireServer bool     `long:"require-server" mapstructure:"require-server" usage:"fail (instead of warn) if the Conduit API server isn't reachable"`
	Quiet         bool     `long:"quiet" short:"q" usage:"suppress passing checks; print only warnings, failures, and the summary"`
	Check         []string `long:"check" usage:"run only the named check(s); repeatable. See --help for the full list of check names"`
	NoColor       bool     `long:"no-color" mapstructure:"no-color" usage:"disable colored output"`
}

// DoctorCommand implements `conduit doctor`. Cfg is exported (matching
// RunCommand's own pattern) so root.go can wire it, though doctor has no
// need to share config with another command the way `conduit config` needs
// run's.
type DoctorCommand struct {
	flags DoctorFlags
	Cfg   conduit.Config

	// out is the stream Render should write glyphs/color for — set from the
	// *cobra.Command ExecuteWithResult receives via context, since
	// CommandWithResult.Render has no writer parameter of its own but still
	// needs to make the same TTY/NO_COLOR decision ui.NewRenderer makes
	// everywhere else. Falls back to os.Stdout if somehow unset (Render
	// called without ExecuteWithResult having run first, which the
	// decorator never does in practice).
	out io.Writer
}

func (c *DoctorCommand) Usage() string { return "doctor" }

func (c *DoctorCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}

	// Mirror run.RunCommand.Flags() exactly (including assigning c.Cfg
	// here, not just a local var): BuildFlags alone gives every
	// conduit.Config-derived flag a zero-value default (""/0/false), and
	// leaves c.Cfg itself zero-valued going into Config()'s ParseConfig
	// call. ParseConfig's viper.Unmarshal only overwrites the fields it has
	// flag/env/file data for — a zero c.Cfg would reach ExecuteWithResult
	// with ConnectorPlugins/ProcessorPlugins still nil (those are plain Go
	// maps, not flags viper knows about), which is exactly wrong for
	// plugins.builtin. Setting c.Cfg = conduit.DefaultConfigWithBasePath(...)
	// here, before ParseConfig ever runs, is what makes it start non-zero —
	// the same reason RunCommand does this in its own Flags(), not Execute.
	c.Cfg = conduit.DefaultConfigWithBasePath(currentPath)
	flags.SetDefault("config.path", c.Cfg.ConduitCfg.Path)
	flags.SetDefault("db.type", c.Cfg.DB.Type)
	flags.SetDefault("db.badger.path", c.Cfg.DB.Badger.Path)
	flags.SetDefault("db.postgres.connection-string", c.Cfg.DB.Postgres.ConnectionString)
	flags.SetDefault("db.postgres.table", c.Cfg.DB.Postgres.Table)
	flags.SetDefault("db.sqlite.path", c.Cfg.DB.SQLite.Path)
	flags.SetDefault("db.sqlite.table", c.Cfg.DB.SQLite.Table)
	flags.SetDefault("api.enabled", c.Cfg.API.Enabled)
	flags.SetDefault("api.http.address", c.Cfg.API.HTTP.Address)
	flags.SetDefault("api.grpc.address", c.Cfg.API.GRPC.Address)
	flags.SetDefault("log.level", c.Cfg.Log.Level)
	flags.SetDefault("log.format", c.Cfg.Log.Format)
	flags.SetDefault("connectors.path", c.Cfg.Connectors.Path)
	flags.SetDefault("connectors.max-receive-record-size", c.Cfg.Connectors.MaxReceiveRecordSize)
	flags.SetDefault("processors.path", c.Cfg.Processors.Path)
	flags.SetDefault("pipelines.path", c.Cfg.Pipelines.Path)
	flags.SetDefault("pipelines.exit-on-degraded", c.Cfg.Pipelines.ExitOnDegraded)
	flags.SetDefault("pipelines.error-recovery.min-delay", c.Cfg.Pipelines.ErrorRecovery.MinDelay)
	flags.SetDefault("pipelines.error-recovery.max-delay", c.Cfg.Pipelines.ErrorRecovery.MaxDelay)
	flags.SetDefault("pipelines.error-recovery.backoff-factor", c.Cfg.Pipelines.ErrorRecovery.BackoffFactor)
	flags.SetDefault("pipelines.error-recovery.max-retries", c.Cfg.Pipelines.ErrorRecovery.MaxRetries)
	flags.SetDefault("pipelines.error-recovery.max-retries-window", c.Cfg.Pipelines.ErrorRecovery.MaxRetriesWindow)
	flags.SetDefault("schema-registry.type", c.Cfg.SchemaRegistry.Type)
	flags.SetDefault("schema-registry.confluent.connection-string", c.Cfg.SchemaRegistry.Confluent.ConnectionString)
	flags.SetDefault("preview.pipeline-arch-v2", c.Cfg.Preview.PipelineArchV2)
	flags.SetDefault("preview.pipeline-arch-v2-disable-metrics", c.Cfg.Preview.PipelineArchV2DisableMetrics)

	return flags
}

func (c *DoctorCommand) Config() ecdysis.Config {
	path := filepath.Dir(c.flags.ConduitCfg.Path)
	return ecdysis.Config{
		EnvPrefix:     "CONDUIT",
		Parsed:        &c.Cfg,
		Path:          c.flags.ConduitCfg.Path,
		DefaultValues: conduit.DefaultConfigWithBasePath(path),
	}
}

func (c *DoctorCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Check whether this machine and configuration are ready for `conduit run`",
		Long: fmt.Sprintf(`doctor runs a set of offline, non-destructive checks against your Conduit
configuration and environment: config resolution and validation, database
reachability, API address availability, plugin directories, the built-in
plugin registry, and whether a running engine is reachable. It answers
"would conduit run succeed here?" — it does not start a Runtime.

This is distinct from the running server's /readyz and /healthz endpoints,
which answer "is the running engine serving?".

Exit codes: 0 if every check passed (warnings never fail the run), 2 if a
check found invalid configuration, 3 if a required dependency (database,
network address, or — with --require-server — the API server) is
unreachable.

Check names (for --check): %s`, strings.Join(sortedCheckNames(), ", ")),
	}
}

func (c *DoctorCommand) ResultCommand() string { return "doctor" }

// doctorResult is Outcome.Result's concrete shape: the full list of check
// results, so both --json (via the Result envelope) and ExitCode (which
// type-asserts it back out of Outcome) see the same data Render does.
type doctorResult struct {
	Checks []check.CheckResult `json:"checks"`
}

func (c *DoctorCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	if cmd := ecdysis.CobraCmdFromContext(ctx); cmd != nil {
		c.out = cmd.OutOrStdout()
	}

	if err := c.validateCheckNames(); err != nil {
		return cecdysis.Outcome{}, err
	}

	exe, _ := os.Executable() // best-effort; standaloneCompatCheck degrades to a Fail if this is empty and --deep is set.
	opts := doctorcheck.Options{
		Deep:          c.flags.Deep,
		RequireServer: c.flags.RequireServer,
		Executable:    exe,
	}

	checks := doctorcheck.DefaultChecks(c.Cfg, log.Nop(), opts)
	checks = filterChecks(checks, c.flags.Check)

	report := check.Run(ctx, checks)

	return cecdysis.Outcome{
		OK:      report.Summary.Failed == 0,
		Summary: report.Summary,
		Result:  doctorResult{Checks: report.Checks},
	}, nil
}

// ExitCode implements cecdysis.CommandWithResultExitCode: it re-derives
// check.Report.ExitCode() from Outcome.Result rather than keeping a
// separate field, so this method is a pure function of what
// ExecuteWithResult already returned (see that interface's doc for why
// this can't just be the RunE error).
func (c *DoctorCommand) ExitCode(outcome cecdysis.Outcome) int {
	res, ok := outcome.Result.(doctorResult)
	if !ok {
		return 0
	}
	return check.Report{Checks: res.Checks}.ExitCode()
}

// validateCheckNames rejects an unknown --check name (or a valid name that
// requires a flag the caller didn't also pass) as a HARD command failure —
// this is a bad invocation, not a domain finding, so it belongs on the
// normal CommandWithResult error path (envelope Error set, exit 2 via
// conduiterr.CodeInvalidArgument).
func (c *DoctorCommand) validateCheckNames() error {
	valid := doctorcheck.CheckNames()
	for _, name := range c.flags.Check {
		if !slices.Contains(valid, name) {
			err := conduiterr.New(conduiterr.CodeInvalidArgument, fmt.Sprintf("unknown check %q", name))
			err.Suggestion = "valid checks: " + strings.Join(sortedCheckNames(), ", ")
			return err
		}
		if name == doctorcheck.NamePluginsStandaloneCompat && !c.flags.Deep {
			err := conduiterr.New(conduiterr.CodeInvalidArgument, fmt.Sprintf("check %q requires --deep", name))
			err.Suggestion = "re-run with --deep, or drop --check " + name
			return err
		}
	}
	return nil
}

// filterChecks narrows checks to just the ones named in selected, in
// checks' original order. An empty selected runs everything (the common
// case, no --check passed).
func filterChecks(checks []check.Check, selected []string) []check.Check {
	if len(selected) == 0 {
		return checks
	}
	want := make(map[string]bool, len(selected))
	for _, name := range selected {
		want[name] = true
	}
	filtered := make([]check.Check, 0, len(checks))
	for _, c := range checks {
		if want[c.Name()] {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

func sortedCheckNames() []string {
	names := append([]string(nil), doctorcheck.CheckNames()...)
	sort.Strings(names)
	return names
}
