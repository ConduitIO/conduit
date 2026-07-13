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

// Package dev implements the file watcher behind `conduit run --dev` (see
// docs/design-documents/20260712-pipeline-dev-hot-reload.md, §4 "Surface").
// It is PR2 of that design: PR1 (merged) added the engine capability this
// package drives — pkg/provisioning.Service.Plan/ApplyPlanLive, which now
// applies a live-eligible diff to a running pipeline in place (a processor
// swap, no restart) and drain-restarts everything else. This package adds no
// new engine behavior; it is purely a debounced directory watcher that turns
// "a file changed on disk" into "call Plan, then ApplyPlanLive" and reports
// the outcome.
//
// # Where this lives
//
// The Watcher is constructed and started by pkg/conduit.Runtime.Run, after
// provisioning.Service.Init has provisioned and started whatever pipelines
// exist at startup — the watcher only ever reacts to *subsequent* edits, and
// is tied to the same serve context startup uses, so Ctrl-C/SIGTERM cancels
// it along with everything else (invariant 7). It is not CLI-layer code:
// cmd/conduit/root/run.RunCommand only flips conduit.Config.Dev.Enabled;
// every byte of watch/debounce/apply logic lives here so the CLI and the
// `conduit pipelines dev` alias can never drift from what the runtime
// actually does. See the design doc's "Three-faces rule": dev is
// legitimately CLI-only — an agent automates the same effect by calling
// ApplyPipeline directly, not by asking a Conduit instance to watch files.
//
// # The operator-authorization gate
//
// ApplyPlanLive refuses to touch a running pipeline unless its caller passes
// allowRestartOnRunning=true (see pkg/provisioning/plan.go's doc on
// CodeLiveApplyUnauthorized) — a Tier-1 data-path gate that exists so an
// unattended/remote caller can never silently restart a live pipeline. The
// Watcher always passes true for the applies IT drives, and only those: the
// human explicitly ran `conduit run --dev` (or `conduit pipelines dev`) and
// is watching each diff scroll by as they save — that interactive act *is*
// the authorization the gate exists to require. This is deliberately
// independent of, and never sets, the process-level
// --api.allow-live-restart-apply flag: the gRPC/HTTP/MCP surface stays gated
// exactly as it was before dev mode existed.
//
// # Ensure-running
//
// ApplyPlanLive's not-currently-running branch imports a pipeline's new
// config without starting it (the correct behavior for provisioning at
// startup, and for a one-shot `conduit pipelines apply`). Dev mode means
// "keep my pipelines running while I iterate", so after every successful
// apply the Watcher calls Start if, and only if, the just-applied config
// wants the pipeline running (config.StatusRunning) — covering a
// brand-new file, and a pipeline left stopped by a prior failed apply. If
// the config declares config.StatusStopped, the Watcher never starts it.
// Start is idempotent from the Watcher's point of view: calling it on an
// already-running pipeline (the common case — most edits target a pipeline
// that ApplyPlanLive already left running, whether via an in-place swap or a
// restart) returns pipeline.ErrPipelineRunning, which the Watcher treats as
// success, not a failure to report.
//
// # Invariants
//
//   - Invariant 7 (graceful shutdown): every long-running goroutine this
//     package starts is derived from, and exits on cancellation of, the
//     context passed to Watcher.Run.
//   - An invalid edit never touches a running pipeline: a file that fails to
//     parse, or a pipeline document that fails config.Enrich/config.Validate,
//     is reported and skipped — Plan/ApplyPlanLive are never called for it.
//   - A deleted watched file never deletes the pipeline it described: the
//     Watcher only logs/reports it and leaves the pipeline exactly as it was.
package dev
