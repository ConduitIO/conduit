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

package dev

import (
	"bytes"
	"context"
	"os"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/config/yaml"
)

// applyFile is invoked once a debounce window settles for path. It never
// panics and never returns an error — every failure is reported through
// w.reporter and logged, because the Watcher must keep running after a bad
// edit (see this package's doc: an invalid edit never touches a running
// pipeline).
//
// The existence check here is what makes atomic-save (rename-over-the
// -file) editors and a genuine file deletion indistinguishable-in-the
// -moment safe to treat identically: by the time the debounce window (300ms
// default) has elapsed with no further fs events, an atomic save has always
// completed (rename(2) is near-instantaneous), so a file that still doesn't
// exist here is, for all practical purposes, actually gone — see debounce.go
// and the design doc's §4 "Debounce/coalesce".
func (w *Watcher) applyFile(ctx context.Context, path string) {
	// A dev apply runs arbitrary, rapidly-edited user YAML through the parser,
	// enrich, validate, and the provisioner — and it runs on a plain goroutine
	// (see debounce.go). An unrecovered panic there would crash the whole server
	// and take every running pipeline down with it: the exact opposite of this
	// package's invariant that a bad edit never touches a running pipeline. Contain
	// it — report the panic as an error event for this file and keep the server
	// (and every live pipeline) up. This is the last-resort backstop; the parse/
	// validate paths are expected to return errors, not panic.
	defer func() {
		if r := recover(); r != nil {
			w.reportRawError(ctx, path, "", cerrors.Errorf("dev: recovered from panic while applying %q: %v", path, r))
		}
	}()

	info, err := os.Stat(path)
	switch {
	case err != nil && os.IsNotExist(err):
		w.handleDeleted(ctx, path)
		return
	case err != nil:
		w.reportRawError(ctx, path, "", cerrors.Errorf("could not stat %q: %w", path, err))
		return
	case !info.Mode().IsRegular():
		// A directory (or other non-regular entry) landed inside the
		// watched directory; nothing to parse.
		return
	}

	pipelines, transient := w.parseFile(ctx, path)
	if transient {
		w.logger.Debug(ctx).Str("path", path).
			Msg("dev: file is empty or unreadable, assuming a transient atomic-save window; waiting for the next event")
		return
	}

	if len(pipelines) == 0 {
		// Either the file failed to parse/validate entirely (already
		// reported by parseFile) or it legitimately defines zero pipelines;
		// either way there is nothing to apply, and — critically — nothing
		// tracked for future deletion reporting is touched: a file that
		// never successfully parsed keeps whatever pipeline IDs (if any) it
		// was last known to define.
		return
	}

	ids := make([]string, 0, len(pipelines))
	for _, p := range pipelines {
		ids = append(ids, p.ID)
		w.applyPipeline(ctx, path, p)
	}

	w.mu.Lock()
	w.filePipelines[path] = ids
	w.mu.Unlock()
}

// parseFile runs the same parse -> enrich -> validate pipeline
// deploy.ParseSinglePipeline/provisioning.Service.provisionPipeline use, but
// over every pipeline document path defines (deploy.ParseSinglePipeline
// rejects anything but exactly one pipeline; dev must not, since a
// pipelines-directory file is free to define several). Every parse or
// validate failure is reported individually (via w.reportRawError) and its
// pipeline excluded from the result — a single bad document must not drop
// the valid ones in the same file (matching
// provisioning.Service.parsePipelineConfigFile's rule, #2255).
//
// transient=true means path could not be read at all as meaningful content
// (missing mid-check or empty) — the Watcher treats this as an in-flight
// atomic save rather than an error worth printing; see applyFile's doc.
func (w *Watcher) parseFile(ctx context.Context, path string) (pipelines []config.Pipeline, transient bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, true
		}
		w.reportRawError(ctx, path, "", cerrors.Errorf("could not read %q: %w", path, err))
		return nil, false
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, true
	}

	parser := yaml.NewParser(w.logger)
	parsed, err := parser.Parse(ctx, bytes.NewReader(data))
	if err != nil {
		// parser.Parse returns a raw cerrors.Join of per-document failures
		// alongside any documents that DID parse — walk it directly (do not
		// wrap it first) so each document's failure is reported as its own
		// event, matching cmd/conduit/internal/validate.validateFile's rule.
		cerrors.ForEach(err, func(e error) { w.reportRawError(ctx, path, "", e) })
	}

	if len(parsed) == 0 && err == nil {
		w.reportRawError(ctx, path, "", cerrors.Errorf("%q defines no pipelines", path))
		return nil, false
	}

	out := make([]config.Pipeline, 0, len(parsed))
	for _, p := range parsed {
		enriched := config.Enrich(p)
		if verr := config.Validate(enriched); verr != nil {
			cerrors.ForEach(verr, func(e error) { w.reportRawError(ctx, path, enriched.ID, e) })
			continue
		}
		out = append(out, enriched)
	}
	return out, false
}

// applyPipeline runs the Plan -> ApplyPlanLive -> ensure-running flow for
// one already-parsed-enriched-validated pipeline and emits exactly one
// Event describing the outcome (OutcomeApplied, OutcomeSkipped, or
// OutcomeError).
func (w *Watcher) applyPipeline(ctx context.Context, path string, desired config.Pipeline) {
	start := time.Now()

	plan, err := w.provisioner.Plan(ctx, desired)
	if err != nil {
		w.reportRawError(ctx, path, desired.ID, cerrors.Errorf("could not plan pipeline %q: %w", desired.ID, err))
		return
	}
	if plan.Empty() {
		w.reporter.Emit(Event{Time: start, Path: path, PipelineID: desired.ID, Outcome: OutcomeSkipped})
		return
	}

	wasRunning := false
	if w.statusFn != nil {
		running, serr := w.statusFn(ctx, desired.ID)
		if serr != nil {
			w.logger.Debug(ctx).Err(serr).Str("pipeline_id", desired.ID).
				Msg("dev: could not determine whether pipeline is currently running; labeling the apply conservatively")
		} else {
			wasRunning = running
		}
	}
	// Pre-apply expectation, used ONLY as a fallback if the engine does not
	// report a ground-truth mode below (an idempotent empty apply detected only
	// inside ApplyPlanLive's re-Plan). The authoritative label comes from the
	// engine's AppliedMode — the plan-derived guess can be wrong, because a
	// live-eligible diff falls back to a restart when a processor cannot be
	// swapped in place (e.g. it runs parallel).
	expectedMode := ModeProvisioned
	if wasRunning {
		if plan.LiveEligible() {
			expectedMode = ModeInPlace
		} else {
			expectedMode = ModeRestart
		}
	}

	// The gate = the interactive invocation: allowRestartOnRunning is always
	// true here because `--dev`/`conduit pipelines dev` running at all IS the
	// operator authorization ApplyPlanLive's gate exists to require — see
	// this package's doc. It does not, and must not, set the process-level
	// --api.allow-live-restart-apply flag; that is a separate, independently
	// -gated surface.
	diff, err := w.provisioner.ApplyPlanLive(ctx, desired, plan.Hash, true)
	dur := time.Since(start)
	if err != nil {
		w.reportRawError(ctx, path, desired.ID, cerrors.Errorf("could not apply pipeline %q: %w", desired.ID, err))
		return
	}

	// Report the mode the engine actually applied, not the pre-apply guess.
	mode := modeFromApplied(diff.AppliedMode, expectedMode)

	started := w.ensureRunning(ctx, desired)

	// Logged in addition to the reporter's --json/human event stream (Out is
	// the CLI-facing surface; this is the same structured log every other
	// engine component uses, for operators who watch container/journal logs
	// rather than dev's own stdout — and it is what lets a test assert "no
	// restart log" without capturing os.Stdout).
	w.logger.Info(ctx).
		Str("pipeline_id", desired.ID).
		Str("path", path).
		Str("mode", string(mode)).
		Bool("started", started).
		Int("changes", len(diff.Changes)).
		Dur("duration", dur).
		Msg("dev: applied")

	w.reporter.Emit(Event{
		Time:       start,
		Path:       path,
		PipelineID: desired.ID,
		Outcome:    OutcomeApplied,
		Mode:       mode,
		Started:    started,
		DurationMS: dur.Milliseconds(),
		Diff:       &diff,
	})
}

// ensureRunning implements the design doc's §4 "Ensure-running": dev mode
// means "keep my pipelines running", so after a successful apply, if desired
// wants the pipeline running, the Watcher starts it — covering both a
// brand-new file (ApplyPlanLive's not-running branch imports without
// starting) and a pipeline left stopped by a prior failed apply. A config
// declaring config.StatusStopped is always left alone: this function is
// never even called with intent to start it (see the desired.Status check
// below), matching "if the config declares it stopped, dev respects that".
//
// Calling Start on an already-running pipeline (by far the common case —
// most edits target a pipeline ApplyPlanLive just left running, whether via
// an in-place swap or a restart) returns pipeline.ErrPipelineRunning, which
// is treated as success-without-action (started=false), not a failure to
// report: ensure-running's job is "make sure it ends up running", and it
// already is.
func (w *Watcher) ensureRunning(ctx context.Context, desired config.Pipeline) bool {
	if desired.Status != config.StatusRunning {
		return false
	}

	err := w.lifecycle.Start(ctx, desired.ID)
	switch {
	case err == nil:
		return true
	case cerrors.Is(err, pipeline.ErrPipelineRunning):
		return false
	default:
		w.reportRawError(ctx, "", desired.ID, cerrors.Errorf("ensure-running: could not start pipeline %q: %w", desired.ID, err))
		return false
	}
}

// handleDeleted reports that path — previously known to define one or more
// pipelines — no longer exists. It never touches those pipelines: they are
// left running exactly as they were (see this package's doc).
func (w *Watcher) handleDeleted(ctx context.Context, path string) {
	w.mu.Lock()
	ids := w.filePipelines[path]
	delete(w.filePipelines, path)
	w.mu.Unlock()

	if len(ids) == 0 {
		w.logger.Debug(ctx).Str("path", path).
			Msg("dev: watched file removed (was not a known pipeline config, or was never successfully applied)")
		return
	}

	now := time.Now()
	for _, id := range ids {
		w.logger.Warn(ctx).Str("path", path).Str("pipeline_id", id).
			Msg("dev: config file removed; pipeline left running")
		w.reporter.Emit(Event{Time: now, Path: path, PipelineID: id, Outcome: OutcomeDeleted})
	}
}

// reportRawError converts err into an OutcomeError Event, logs it (see
// applyPipeline's success-path logging for why: the same structured log
// every other engine component uses, for operators/tests that watch logs
// rather than dev's own Out stream), and emits the Event. pipelineID may be
// empty (a file-level failure, e.g. an unparseable document, is not yet
// attributable to any one pipeline ID). It never touches the pipeline this
// error names, if any — see this package's doc: an invalid edit is reported,
// not applied.
func (w *Watcher) reportRawError(ctx context.Context, path, pipelineID string, err error) {
	info := errorInfoFromErr(err)
	w.logger.Warn(ctx).
		Str("path", path).
		Str("pipeline_id", pipelineID).
		Str("code", info.Code).
		Msg("dev: " + info.Message)
	w.reporter.Emit(Event{
		Time:       time.Now(),
		Path:       path,
		PipelineID: pipelineID,
		Outcome:    OutcomeError,
		Error:      &info,
	})
}
