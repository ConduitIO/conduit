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

// Package mcp implements `conduit mcp`: the ecdysis command that runs the
// agent-native MCP server (see
// docs/design-documents/20260708-mcp-server.md). This package owns only the
// transport/process concerns — flags, stdio vs. HTTP, HTTP auth/TLS; the
// tool catalog itself (what each tool does) lives in
// cmd/conduit/internal/mcp, which this command is a thin cobra shell over,
// matching every other CLI-verb/engine split in this codebase.
package mcp

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	mcpengine "github.com/conduitio/conduit/cmd/conduit/internal/mcp"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/ecdysis"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
)

var (
	_ ecdysis.CommandWithFlags   = (*MCPCommand)(nil)
	_ ecdysis.CommandWithExecute = (*MCPCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*MCPCommand)(nil)
	_ ecdysis.CommandWithConfig  = (*MCPCommand)(nil)
)

// MCPFlags holds MCPCommand's flags. conduit.Config is embedded so
// deploy/apply/doctor (wrapped 1:1 by the mcp tool catalog) read the same
// db.*/pipelines.path/... configuration `conduit pipelines deploy`/`apply`
// and `conduit doctor` do — mirrors cmd/conduit/root/pipelines DeployFlags'
// own reasoning exactly.
type MCPFlags struct {
	conduit.Config

	// H-1 (design doc 20260712-mcp-http-transport.md, AC-11/SR-1): this usage
	// string previously said HTTP is served "in addition to stdio" — false,
	// since Execute serves HTTP-only in --http mode (a daemon has no
	// attached stdin to also read stdio from). Keep this text in sync with
	// Execute's actual branch.
	HTTP string `long:"http" usage:"serve the streamable-HTTP transport on this address INSTEAD OF stdio (EXPERIMENTAL; see docs/operations/mcp-server.md); requires --token-file and --tls-cert/--tls-key"`
	// AllowMutations is a startup/process flag, deliberately not a tool
	// argument — see cmd/conduit/internal/mcp.Config.AllowMutations's doc.
	AllowMutations bool   `long:"allow-mutations" usage:"register the write tools (apply, scaffold_connector, scaffold_processor); an operator/process-level switch, never agent-settable"`
	TokenFile      string `long:"token-file" usage:"path to a file containing the bearer token --http requires (compared constant-time)"`
	TLSCert        string `long:"tls-cert" usage:"TLS certificate file for --http"`
	TLSKey         string `long:"tls-key" usage:"TLS key file for --http"`
	APIAddress     string `long:"api-address" usage:"gRPC address of a running Conduit, dialed by the inspect tool"`
}

// MCPCommand implements `conduit mcp`: a long-running server, like `conduit
// run` — it has no discrete --json result (see run.RunCommand for the same
// ecdysis.CommandWithExecute shape).
type MCPCommand struct {
	flags MCPFlags
	// Cfg mirrors DoctorCommand/RunCommand's own exported Cfg field: Flags()
	// sets non-zero defaults on it before ParseConfig runs (see Flags' doc),
	// and Execute reads the parsed result back out of flags.Config, not Cfg
	// — Cfg exists only so root.go could share it with another command the
	// way `conduit config` shares run's, if that's ever needed for mcp.
	Cfg conduit.Config
}

func (c *MCPCommand) Usage() string { return "mcp" }

func (c *MCPCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Run Conduit's MCP server, exposing pipeline operations to AI agents",
		Long: `Registers Conduit's operations as MCP tools that are 1:1 with the CLI verbs and call the
exact same engines — validate/lint/dry_run/deploy/doctor/inspect are always available (deploy only
computes a diff + hash, never mutates); apply/scaffold_connector/scaffold_processor are additionally
registered only when --allow-mutations is set, since those tools mutate the local pipeline store or
filesystem. --allow-mutations is an operator/process-level flag, never something an agent can pass as
a tool argument.

Serves stdio by default (the primary agent channel — no auth needed, since the agent owns the
process). Pass --http <addr> to serve the streamable-HTTP transport INSTEAD OF stdio — this is a
network-daemon mode (systemd/container) with no attached stdin, so it does not also serve stdio;
--http requires both --token-file (a bearer token, compared constant-time) and
--tls-cert/--tls-key — it refuses to start with either missing (fail-closed: no plaintext, no
unauthenticated HTTP). The HTTP transport is EXPERIMENTAL: no rate limiting, no per-agent tokens,
and a single shared token with no rotation short of a restart — see
docs/operations/mcp-server.md.

The inspect tool requires a running Conduit: pass --api-address to point it at one.`,
		Example: "conduit mcp\n" +
			"conduit mcp --api-address localhost:8084\n" +
			"conduit mcp --allow-mutations\n" +
			"conduit mcp --http :8443 --token-file token.txt --tls-cert cert.pem --tls-key key.pem",
	}
}

func (c *MCPCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}

	// Mirror RunCommand.Flags()/DoctorCommand.Flags()/storeConfigDefaults
	// (cmd/conduit/root/pipelines/deploy.go, unexported to that package)
	// exactly: BuildFlags alone leaves every conduit.Config-derived flag at
	// its Go zero value, and c.flags.Config zero-valued going into
	// Config()'s ParseConfig call — wrong for the same reasons documented
	// on those commands' own Flags methods.
	c.Cfg = conduit.DefaultConfigWithBasePath(currentPath)
	flags.SetDefault("config.path", c.Cfg.ConduitCfg.Path)
	flags.SetDefault("db.type", c.Cfg.DB.Type)
	flags.SetDefault("db.badger.path", c.Cfg.DB.Badger.Path)
	flags.SetDefault("db.postgres.connection-string", c.Cfg.DB.Postgres.ConnectionString)
	flags.SetDefault("db.postgres.table", c.Cfg.DB.Postgres.Table)
	flags.SetDefault("db.sqlite.path", c.Cfg.DB.SQLite.Path)
	flags.SetDefault("db.sqlite.table", c.Cfg.DB.SQLite.Table)
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
	flags.SetDefault("api-address", c.Cfg.API.GRPC.Address)

	return flags
}

func (c *MCPCommand) Config() ecdysis.Config {
	path := filepath.Dir(c.flags.ConduitCfg.Path)
	return ecdysis.Config{
		EnvPrefix:     "CONDUIT",
		Parsed:        &c.flags.Config,
		Path:          c.flags.ConduitCfg.Path,
		DefaultValues: conduit.DefaultConfigWithBasePath(path),
	}
}

// Execute runs the MCP server until ctx is done (or a transport fails
// unrecoverably). It never returns a result to render — like RunCommand, it
// is a long-running process command, not a CommandWithResult.
func (c *MCPCommand) Execute(ctx context.Context) error {
	srv := mcpengine.NewServer(mcpengine.Config{
		AllowMutations: c.flags.AllowMutations,
		StoreConfig:    c.flags.Config,
		APIAddress:     c.flags.APIAddress,
	})

	if c.flags.HTTP == "" {
		// stdio only — the common case: the agent owns this process, so no
		// separate auth is needed (design doc §5).
		return srv.Run(ctx, &sdkmcp.StdioTransport{})
	}

	httpSrv, err := c.newHTTPServer(srv, c.httpLogger())
	if err != nil {
		return err
	}

	// HTTP mode serves the network transport ONLY — it does not co-run stdio.
	// `--http` is the network-daemon mode (systemd/container), which has no
	// attached stdin; co-running stdio there would hit EOF immediately and
	// could tear the whole errgroup (and the HTTP server) down on startup. The
	// stdio transport is the default, no-`--http` path above, for the
	// agent-owns-the-subprocess case.
	grp, gctx := errgroup.WithContext(ctx)
	grp.Go(func() error {
		<-gctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	})
	grp.Go(func() error {
		err := httpSrv.ListenAndServeTLS("", "") // cert/key already loaded into httpSrv.TLSConfig
		if cerrors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})

	return grp.Wait()
}

// httpLogger builds the log.CtxLogger the --http path uses for its startup/
// auth-event logging (design doc H-2/H-4). It mirrors
// pkg/conduit/runtime.go's own newLogger construction (same log.level/
// log.format config, same zerolog.ParseLevel/log.ParseFormat calls) rather
// than importing it: MCPCommand does not build a conduit.Runtime, and
// pulling in that dependency for two lines of zerolog setup would be a
// heavier coupling than the duplication here. Malformed level/format values
// fall back to zerolog's/log's zero values (InfoLevel, FormatCLI) rather
// than failing --http startup over a logging config typo.
func (c *MCPCommand) httpLogger() log.CtxLogger {
	level, err := zerolog.ParseLevel(c.flags.Config.Log.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}
	format, err := log.ParseFormat(c.flags.Config.Log.Format)
	if err != nil {
		format = log.FormatCLI
	}
	return log.InitLogger(level, format)
}
