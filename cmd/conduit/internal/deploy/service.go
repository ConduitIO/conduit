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

package deploy

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
)

// CodeStandaloneUnsafe is raised when NewLocalService refuses to construct a
// standalone PlanApplier because it cannot make apply's running-pipeline
// safety check (Invariant 7 / AC-13) trustworthy for the configured store —
// see NewLocalService's doc.
var CodeStandaloneUnsafe = conduiterr.Register("provisioning.standalone_unsafe", codes.FailedPrecondition)

// NewLocalService opens cfg's configured store directly and returns a
// PlanApplier (a real *pkg/provisioning.Service, via conduit.NewRuntime) for
// the standalone `conduit pipelines deploy|apply` CLI commands to Plan/
// ApplyPlan against, plus a cleanup func the caller must always call.
//
// # Known limitation: apply's running-pipeline check outside BadgerDB/SQLite
//
// ApplyPlan's Invariant-7 guard (see pkg/provisioning/plan.go's
// isRunningStatus) refuses to mutate a pipeline it believes is running. That
// belief comes from pipelineService.Init, which — because it cannot know
// whether the process that last marked a pipeline StatusRunning is still
// alive — launders any persisted StatusRunning into StatusSystemStopped
// (the same behavior conduit run itself relies on after a real crash/restart).
// So a status read through *this* Runtime can never distinguish "was running
// before an unrelated restart" from "is actually running right now in a
// separate, live conduit run process" — only the process that actually
// started a pipeline's goroutines (or a live RPC to it) can tell those apart.
//
// Only BadgerDB closes this by a structural side effect: it takes an exclusive
// lock on its store directory, so if a conduit run process is genuinely running
// against the same store, OpenStore below fails outright (the standalone command
// never gets to run at all) instead of silently proceeding with a stale status.
// No other store type has that property — SQLite opens in WAL mode
// (journal_mode=WAL, busy_timeout), which is explicitly designed for concurrent
// multi-process access and does not fail closed; Postgres allows concurrent
// connections with no store lock at all; InMemory gives each process a private
// empty store, so a standalone apply can't even see the running server's state.
// So NewLocalService refuses every store type except Badger (default-deny)
// rather than allow-list "safe" ones — refusing outright with
// CodeStandaloneUnsafe beats constructing a PlanApplier whose AC-13 check could
// be silently wrong. An embedder that has externally guaranteed exclusive access
// can still Plan/ApplyPlan against a *pkg/provisioning.Service it builds itself.
// Closing this gap for real (a live-server RPC, so the running-pipeline check is
// answered by the process that actually owns the pipeline) is tracked follow-up.
func NewLocalService(ctx context.Context, cfg conduit.Config) (PlanApplier, func() error, error) {
	if cfg.DB.Type != conduit.DBTypeBadger {
		ce := conduiterr.New(CodeStandaloneUnsafe, fmt.Sprintf(
			"deploy/apply cannot safely run standalone against a %q store: a separate 'conduit run' process may have "+
				"this pipeline open, and only BadgerDB's exclusive store lock lets this command detect that "+
				"(SQLite's WAL mode and Postgres both allow concurrent multi-process access)", cfg.DB.Type))
		ce.Suggestion = "use a BadgerDB store, or apply changes through a conduit run instance you control directly (a live-server RPC path is tracked follow-up work)"
		return nil, func() error { return nil }, ce
	}

	// deploy/apply are one-shot CLI commands whose stdout is the rendered
	// plan (or a single JSON object under --json — CLI output conventions
	// §1 requires that be the only thing on stdout, or a --json consumer's
	// parse breaks). conduit.NewRuntime's default logger
	// (pkg/foundation/log.InitLogger) writes unconditionally to os.Stdout,
	// which is fine for the long-running `conduit run` server (it has no
	// competing machine-readable stdout contract) but would interleave
	// runtime log lines into deploy/apply's stdout. Route it to stderr
	// instead, at the same level/format the user configured, before
	// constructing the Runtime.
	cfg.Log.NewLogger = stderrLogger

	rt, err := conduit.NewRuntime(cfg)
	if err != nil {
		return nil, func() error { return nil }, err
	}

	if err := rt.InitProvisioningOnly(ctx); err != nil {
		_ = rt.DB.Close()
		return nil, func() error { return nil }, err
	}

	return rt.ProvisionService, rt.DB.Close, nil
}

// stderrLogger mirrors pkg/foundation/log.InitLogger (the default
// conduit.Config.Log.NewLogger) exactly, except its writer targets os.Stderr
// instead of os.Stdout — see NewLocalService's assignment site for why.
func stderrLogger(level, format string) log.CtxLogger {
	// Errors are intentionally ignored here, matching log.InitLogger's own
	// zero-value-on-error behavior (an unparseable level/format falls back
	// to zerolog's zero values rather than failing service construction).
	l, _ := zerolog.ParseLevel(level)
	f, _ := log.ParseFormat(format)

	var w io.Writer = os.Stderr
	if f == log.FormatCLI {
		cw := zerolog.NewConsoleWriter(func(cw *zerolog.ConsoleWriter) { cw.Out = os.Stderr })
		cw.TimeFormat = "2006-01-02T15:04:05+00:00"
		w = cw
	}

	logger := zerolog.New(w).With().Timestamp().Stack().Logger().Level(l)
	return log.New(logger)
}
