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

// Package provisioning's plan.go implements the preview/diff engine behind
// `conduit pipelines deploy|apply` (see
// docs/design-documents/20260708-cli-pipeline-deploy-apply.md). It adds no
// new reconcile logic: Plan runs the exact same Export -> actionsBuilder.Build
// the existing importPipeline path runs (see import.go), and simply
// *describes* the resulting actions instead of executing them. ApplyPlan
// re-runs Plan to recompute a fresh Diff, gates on the caller presenting the
// hash of the plan it actually reviewed, and only then reuses importPipeline
// verbatim — so the plan a human/agent approved and the plan that executes
// are, by construction, the same action list (no drift between preview and
// apply).
//
// Invariant 7 (graceful shutdown / no silent mutation of live work): ApplyPlan
// refuses to run against a pipeline that is currently running rather than
// attempting an in-process stop-drain-restart (see the design doc's Failure
// modes, option (a)) — the safe Wave-2 default for the standalone (no live
// lifecycle) case. Callers that want to apply a restart-class change to a
// running pipeline via ApplyPlan must stop it first.
//
// ApplyPlanLive (added for issue #2588, see
// docs/design-documents/20260708-live-server-deploy-apply.md) is the
// live-server counterpart used only where a running lifecycle.Service is
// available: instead of refusing, it drives lifecycleService.StopAndWait to
// gracefully drain and durably checkpoint the pipeline before reusing the
// same importPipeline path, then restarts it. ApplyPlan itself is unchanged
// and keeps refusing running pipelines — it has no lifecycle to drive.
package provisioning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	json "github.com/goccy/go-json"
	"github.com/google/go-cmp/cmp"
)

// Resource identifies the kind of entity a Change describes.
type Resource string

const (
	ResourcePipeline  Resource = "pipeline"
	ResourceConnector Resource = "connector"
	ResourceProcessor Resource = "processor"
)

// ChangeAction identifies what applying a Change would do to Resource/ID.
type ChangeAction string

const (
	ChangeActionCreate ChangeAction = "create"
	ChangeActionUpdate ChangeAction = "update"
	ChangeActionDelete ChangeAction = "delete"
)

// Effect classifies whether a Change can be applied without disrupting a
// running pipeline (EffectInPlace) or requires stopping/recreating it
// (EffectRestart). See action.Describe's doc on import_actions.go for the
// per-action classification rules, and Plan's doc for the brand-new-pipeline
// override.
type Effect string

const (
	EffectInPlace Effect = "in_place"
	EffectRestart Effect = "restart"
)

// Change is a stable, human- and --json-renderable description of one action
// Plan computed and ApplyPlan would execute. It intentionally carries no
// From/To value pairs: ConfigPaths names *which* fields changed (e.g.
// "settings.table") without embedding the values themselves, since connector
// Settings routinely contain credentials — a plan/diff surface is not the
// place to echo them back, in output or in the hash.
type Change struct {
	Resource    Resource     `json:"resource"`
	ID          string       `json:"id"`
	Action      ChangeAction `json:"action"`
	Effect      Effect       `json:"effect"`
	ConfigPaths []string     `json:"configPaths,omitempty"`
	// Code is a stable, dotted identifier for this kind of change (e.g.
	// "provisioning.connector.update"), namespaced the same way
	// conduiterr.Code reasons are, for consistent agent/UI consumption.
	Code string `json:"code"`
}

// Diff is Plan's result: every Change needed to reconcile the pipeline
// currently stored with the desired config, plus a Hash binding this exact
// Diff — ApplyPlan refuses to run unless the caller presents this Hash.
type Diff struct {
	PipelineID string   `json:"pipelineID"`
	Changes    []Change `json:"changes"`
	Hash       string   `json:"hash"`
}

// Empty reports whether the Diff has no changes, i.e. the desired config
// already matches the current state (an idempotent re-apply).
func (d Diff) Empty() bool { return len(d.Changes) == 0 }

// computeHash returns a deterministic digest of PipelineID, Changes (in the
// order actionsBuilder.Build produced them — the same order ApplyPlan would
// execute them in, not re-sorted, since the hash must be over exactly what
// would run) and the full desired config.
//
// desired is included even though Change/ConfigPaths deliberately omit field
// values (see Change's doc, on secrets): without it, two different desired
// configs that happen to produce the same *shape* of diff against the
// current state — e.g. the same connector's "settings.table" changing from
// "a"->"b" in one file and "a"->"c" in another — would hash identically.
// That would both violate "changing the file changes the hash" and, worse,
// weaken the anti-replay guarantee ApplyPlan's hash check exists for: a
// caller could present a hash computed for one desired config and apply a
// different one that happens to collide. Hash is a one-way SHA-256 digest —
// never rendered or decodable — so folding desired's raw values (which may
// include connector credentials) into it does not leak them the way
// including them in Change/ConfigPaths would.
func (d Diff) computeHash(desired config.Pipeline) string {
	type hashable struct {
		PipelineID string          `json:"pipelineID"`
		Changes    []Change        `json:"changes"`
		Desired    config.Pipeline `json:"desired"`
	}
	b, err := json.Marshal(hashable{PipelineID: d.PipelineID, Changes: d.Changes, Desired: desired})
	if err != nil {
		// Change/Diff/config.Pipeline are plain structs of strings, ints,
		// and maps/slices thereof; Marshal cannot fail for them. A failure
		// here would be a bug in this package, not a runtime condition a
		// caller could recover from — fail loudly rather than silently
		// return an empty/wrong hash that ApplyPlan would then treat as a
		// valid token.
		panic(fmt.Sprintf("provisioning: could not marshal diff for hashing: %v", err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Plan computes the Diff needed to reconcile the currently stored pipeline
// (via Export) with desired, without executing anything — Plan never calls
// action.Do, so it has no side effects and is safe to call at any time,
// including against a running pipeline.
//
// It runs the exact same actionsBuilder.Build(old, new) the apply path runs
// (see importPipeline), then maps each resulting action to a Change via
// action.Describe — so the preview and the apply are the same action list by
// construction; there is no separate "describe" code path that could drift
// from what ApplyPlan actually executes.
func (s *Service) Plan(ctx context.Context, desired config.Pipeline) (Diff, error) {
	oldConfig, err := s.Export(ctx, desired.ID)
	if err != nil && !cerrors.Is(err, pipeline.ErrInstanceNotFound) {
		return Diff{}, cerrors.Errorf("could not export current state of pipeline %v, this could mean the Conduit state is corrupted: %w", desired.ID, err)
	}

	actions := s.newActionsBuilder().Build(oldConfig, desired)

	changes := make([]Change, 0, len(actions))
	for _, a := range actions {
		changes = append(changes, a.Describe())
	}

	if oldConfig.ID == "" {
		// Invariant: a pipeline that does not exist yet has nothing running
		// to disrupt. Individual actions' Describe() classify create/delete
		// of a connector or processor as EffectRestart because *in general*
		// (an existing, possibly-running pipeline) adding/removing a
		// resource changes a live topology — see import_actions.go. That
		// general-case classification does not apply to a first-time
		// deploy, so Plan overrides every Change's Effect here, using
		// information (oldConfig) that individual actions don't otherwise
		// need to carry.
		for i := range changes {
			changes[i].Effect = EffectInPlace
		}
	}

	d := Diff{PipelineID: desired.ID, Changes: changes}
	d.Hash = d.computeHash(desired)
	return d, nil
}

// ApplyPlan recomputes Plan(ctx, desired) and refuses to run unless hash
// matches the freshly computed plan's hash exactly — the token binding an
// approved plan to its execution (design doc §2). A mismatch means the
// config file or the live pipeline state changed since the plan was shown,
// and is reported as a *conduiterr.ConduitError coded CodePlanStale.
//
// It returns the freshly computed Diff in every case (even on a stale-hash
// or running-pipeline refusal) so a caller can render "here's what actually
// changed" without a second Plan call.
//
// Invariant 7 / Tier-1 safety (see the design doc's AC-13 and "Failure
// modes"): before executing anything, ApplyPlan checks whether the target
// pipeline is currently running and refuses with CodePipelineRunning if so,
// rather than silently mutating or attempting an in-process stop-drain
// -restart. This is the Wave-2 safe default (design doc, recommendation
// (a)); an operator must stop the pipeline first. The check is skipped when
// there is nothing to do (Diff.Empty()) so a no-op re-apply against a
// running pipeline stays idempotent instead of being needlessly refused.
func (s *Service) ApplyPlan(ctx context.Context, desired config.Pipeline, hash string) (Diff, error) {
	fresh, err := s.Plan(ctx, desired)
	if err != nil {
		return Diff{}, err
	}

	if fresh.Hash != hash {
		ce := conduiterr.New(CodePlanStale, fmt.Sprintf(
			"plan for pipeline %q is stale: the presented hash %q does not match the current plan hash %q; "+
				"the config file or the live pipeline state changed since the plan was computed", desired.ID, hash, fresh.Hash))
		ce.Suggestion = "re-run 'conduit pipelines deploy' to compute a fresh plan, review it, then apply its hash"
		return fresh, ce
	}

	if fresh.Empty() {
		// Idempotent: desired already matches current state. No actions to
		// run, so the running-pipeline guard below is also skipped — there
		// is nothing that could mutate a live pipeline here.
		return fresh, nil
	}

	current, err := s.pipelineService.Get(ctx, desired.ID)
	if err != nil && !cerrors.Is(err, pipeline.ErrInstanceNotFound) {
		return fresh, cerrors.Errorf("could not check whether pipeline %v is running: %w", desired.ID, err)
	}
	if err == nil && isRunningStatus(current.GetStatus()) {
		ce := conduiterr.New(CodePipelineRunning, fmt.Sprintf(
			"pipeline %q is running; apply refuses to mutate a live pipeline", desired.ID))
		ce.Suggestion = fmt.Sprintf("stop the pipeline first (conduit pipelines stop %s), then re-run apply", desired.ID)
		return fresh, ce
	}

	if err := s.transactionalImport(ctx, desired); err != nil {
		return fresh, err
	}
	return fresh, nil
}

// transactionalImport wraps importPipeline in a single DB transaction, exactly
// as provisionPipeline does (service.go). Without it, importPipeline's actions
// each commit their own writes (and a single action can write more than once,
// e.g. createPipelineAction does Create then UpdateDLQ), so a crash — or an
// error whose in-process reverse rollback itself fails — could leave the store
// partially mutated with no recovery on restart. The transaction makes the
// import all-or-nothing: on any error this returns before Commit and the
// deferred Discard drops every write. Invariant 5: state writes are atomic.
//
// Shared by ApplyPlan and ApplyPlanLive so the two apply paths can't drift on
// crash-safety — see docs/design-documents/20260708-live-server-deploy-apply.md,
// "Review outcome & required rework", blocker 2 (landed as #2595).
func (s *Service) transactionalImport(ctx context.Context, desired config.Pipeline) error {
	txn, importCtx, err := s.db.NewTransaction(ctx, true)
	if err != nil {
		return cerrors.Errorf("could not create db transaction: %w", err)
	}
	defer txn.Discard()

	if err := s.importPipeline(importCtx, desired, pipeline.ProvisionTypeConfig); err != nil {
		return err
	}

	if err := txn.Commit(); err != nil {
		return cerrors.Errorf("could not commit db transaction: %w", err)
	}
	return nil
}

// ApplyPlanLive is ApplyPlan's Tier-1 live-server counterpart (design doc §2,
// as reworked): it recomputes and hash-verifies the plan exactly like
// ApplyPlan, but where ApplyPlan refuses a running pipeline outright,
// ApplyPlanLive drives the pipeline's own lifecycle to apply the change
// safely:
//
//  1. If the pipeline is not currently running (or does not exist yet), this
//     is identical to ApplyPlan's tail: transactionalImport and return. There
//     is nothing live to disrupt.
//  2. If the pipeline is running, ApplyPlanLive calls
//     lifecycleService.StopAndWait (never Stop — see that method's doc for
//     why: only StopAndWait proves the pipeline has fully drained AND its
//     positions are durably persisted before anything mutates it).
//  3. Once StopAndWait returns, transactionalImport runs exactly as in step 1
//     — the pipeline is now stopped and quiescent, so this is safe.
//  4. Only if the import commits does ApplyPlanLive call
//     lifecycleService.Start to resume the pipeline under the new config,
//     which resumes from the checkpointed position each connector's Open
//     reads on startup (invariant 2: at-least-once).
//
// Failure handling (design doc "Failure modes" + AC-5/AC-6): on any error
// after StopAndWait — whether the import fails (importPipeline's own reverse
// rollback already ran, see import.go) or Start itself fails — ApplyPlanLive
// returns the error and does NOT attempt to (re)start the pipeline. It is
// left stopped rather than auto-started into a half-applied or just-rolled-
// back state; an operator/agent can inspect the error and retry Start
// explicitly once they've confirmed the pipeline's state. This is the
// property that makes a crash between StopAndWait and Start (or mid-import)
// recoverable: the transaction (step 3) makes the store consistent
// (all-or-nothing), the pipeline is left stopped either way, and a subsequent
// Start — whenever it happens — resumes from the last durable checkpoint.
//
// Scope decision (design doc §2, left open there — "may still stop-restart
// for safety"): ApplyPlanLive does NOT branch on individual Changes' Effect
// (EffectInPlace vs EffectRestart) — every non-empty Diff against a running
// pipeline goes through the full stop-drain-restart, even a diff that is
// EffectInPlace-only (e.g. a connector settings change). A finer-grained "no
// stop needed for in-place-only changes" optimization is explicitly deferred
// to true in-place hot-reload (§4 of the design doc, out of scope for
// #2588 PR1): inventing a narrower classifier here, ahead of that design
// work, risks silently under-draining a change that looks in-place but isn't
// (see the design doc's Item 6 rework, which made the same conservative call
// for the operator-authorization gate).
func (s *Service) ApplyPlanLive(ctx context.Context, desired config.Pipeline, hash string) (Diff, error) {
	fresh, err := s.Plan(ctx, desired)
	if err != nil {
		return Diff{}, err
	}

	if fresh.Hash != hash {
		ce := conduiterr.New(CodePlanStale, fmt.Sprintf(
			"plan for pipeline %q is stale: the presented hash %q does not match the current plan hash %q; "+
				"the config file or the live pipeline state changed since the plan was computed", desired.ID, hash, fresh.Hash))
		ce.Suggestion = "re-run 'conduit pipelines deploy' to compute a fresh plan, review it, then apply its hash"
		return fresh, ce
	}

	if fresh.Empty() {
		// Idempotent: desired already matches current state. Nothing to apply,
		// so there is nothing that could disrupt a live pipeline here either.
		return fresh, nil
	}

	current, err := s.pipelineService.Get(ctx, desired.ID)
	if err != nil && !cerrors.Is(err, pipeline.ErrInstanceNotFound) {
		return fresh, cerrors.Errorf("could not check whether pipeline %v is running: %w", desired.ID, err)
	}
	running := err == nil && isRunningStatus(current.GetStatus())

	if !running {
		// Nothing live to disrupt — identical to ApplyPlan's tail.
		if err := s.transactionalImport(ctx, desired); err != nil {
			return fresh, err
		}
		return fresh, nil
	}

	// Invariant 7 / Tier-1 safety: StopAndWait — not Stop — is the only
	// primitive that proves the pipeline is fully drained and its positions
	// are durably persisted before importPipeline runs against it. See
	// lifecycle.Service.StopAndWait's doc and the design doc's blocker 1.
	//
	// KNOWN GAP (flagged, not fixed, in PR1 of #2588 — see the design doc's
	// Failure modes, "Concurrent applies / apply-during-manual-stop", which
	// calls for a per-pipeline provisioning lock this method does not yet
	// take): between StopAndWait returning and transactionalImport/Start
	// below, nothing prevents a concurrent caller from independently calling
	// Start on this now-genuinely-stopped pipeline. If that happens, this
	// method's own Start call below fails (Start refuses an
	// already-running pipeline), the store nonetheless already has the new
	// config (transactionalImport committed), and the concurrently-started
	// pipeline keeps running with the connector instances it loaded before
	// the mutation — a config-drift condition (not a data-loss one:
	// invariants 1-3 aren't violated) until it's stopped and started again.
	// Not reachable from outside this package in PR1 (no API/CLI/MCP surface
	// calls ApplyPlanLive yet), so there is no live exploitability today —
	// but a per-pipeline lock belongs in or before whichever PR first exposes
	// this method to concurrent external callers (tracked for PR2).
	if err := s.lifecycleService.StopAndWait(ctx, desired.ID); err != nil {
		return fresh, cerrors.Errorf("could not stop pipeline %q to apply live changes: %w", desired.ID, err)
	}

	if err := s.transactionalImport(ctx, desired); err != nil {
		// The pipeline is already stopped (StopAndWait above) and the
		// transaction guarantees the store wasn't left partially mutated
		// (invariant 5). Leave it stopped — do not attempt to restart into a
		// half-applied or rolled-back state — and surface the error as-is.
		return fresh, err
	}

	if err := s.lifecycleService.Start(ctx, desired.ID); err != nil {
		// The new config is already durably committed; only the restart
		// failed. Leave the pipeline stopped with the valid new config and
		// surface the error so an operator/agent can retry Start once the
		// underlying issue (e.g. a bad plugin path) is resolved.
		return fresh, cerrors.Errorf("pipeline %q was updated but failed to restart, it remains stopped with the new config: %w", desired.ID, err)
	}

	return fresh, nil
}

// isRunningStatus reports whether status represents a pipeline with live,
// in-process work that apply must not silently disrupt. StatusRecovering and
// StatusDegraded are included alongside StatusRunning: both mean at least
// one connector/processor goroutine may still be active (recovering from,
// or having partially failed into, an error), so the same invariant-7
// concern applies — only a pipeline a human/system has actually stopped
// (StatusUserStopped, StatusSystemStopped) is safe to mutate.
//
// KNOWN GAP (pre-existing, not introduced by ApplyPlanLive, flagged during
// its review): a pipeline that reached StatusDegraded via a fatal error (or
// exhausted error-recovery retries) has, by the time that status is visible
// here, already removed itself from lifecycle.Service's runningPipelines —
// see Service.runPipeline's cleanup goroutine in pkg/lifecycle/service.go,
// which calls UpdateStatus(StatusDegraded, ...) and runningPipelines.Delete
// in the same terminal step. So isRunningStatus correctly reports "true"
// (there was live work, and the pipeline needs an explicit decision before
// being mutated) but StopAndWait/Stop then fails with
// pipeline.ErrPipelineNotRunning against that same pipeline, since it is no
// longer registered as running. ApplyPlan has the identical dead end today
// (refuses with CodePipelineRunning, but the suggested fix — "stop the
// pipeline first" — also fails for the same reason). Fixing this requires
// either excluding StatusDegraded here or making Stop/StopAndWait tolerant of
// an already-self-stopped Degraded pipeline; out of scope for this change,
// surfaced for an explicit decision rather than silently left as a
// non-obvious dead end.
func isRunningStatus(status pipeline.Status) bool {
	switch status {
	case pipeline.StatusRunning, pipeline.StatusRecovering, pipeline.StatusDegraded:
		return true
	case pipeline.StatusSystemStopped, pipeline.StatusUserStopped:
		return false
	default:
		return false
	}
}

// -----------------------------------------------------------------------
// -- Describe() helpers shared by the action implementations below --
// -----------------------------------------------------------------------

// diffPipelineFields returns the sorted, deterministic list of
// config.PipelineMutableFields (lower-cased JSON-path style names) that
// differ between oldCfg and newCfg. It deliberately mirrors
// actionsBuilder.preparePipelineActions's own cmp.Equal check (same ignored
// field: Status) so a Change's ConfigPaths can never claim a field changed
// that the builder itself considered equal.
func diffPipelineFields(oldCfg, newCfg config.Pipeline) []string {
	var paths []string
	if oldCfg.Name != newCfg.Name {
		paths = append(paths, "name")
	}
	if oldCfg.Description != newCfg.Description {
		paths = append(paths, "description")
	}
	if !equalConnectorIDs(oldCfg.Connectors, newCfg.Connectors) {
		paths = append(paths, "connectors")
	}
	if !equalProcessorIDs(oldCfg.Processors, newCfg.Processors) {
		paths = append(paths, "processors")
	}
	if !cmp.Equal(oldCfg.DLQ, newCfg.DLQ) {
		paths = append(paths, "dlq")
	}
	return paths
}

// diffConnectorFields returns the sorted list of changed field names for a
// connector update, expanding a changed Settings map into one
// "settings.<key>" entry per differing key (added, removed, or changed
// value) rather than a single opaque "settings" entry, matching the design
// doc's UX example ("settings.table: users -> orders_v2" — see the doc for
// why the value pair itself is intentionally not carried here).
func diffConnectorFields(oldCfg, newCfg config.Connector) []string {
	var paths []string
	if oldCfg.Name != newCfg.Name {
		paths = append(paths, "name")
	}
	if oldCfg.Plugin != newCfg.Plugin {
		paths = append(paths, "plugin")
	}
	if !equalProcessorIDs(oldCfg.Processors, newCfg.Processors) {
		paths = append(paths, "processors")
	}
	paths = append(paths, settingsDiffPaths(oldCfg.Settings, newCfg.Settings)...)
	return paths
}

// diffProcessorFields returns the sorted list of changed field names for a
// processor update. Processors have no immutable-field classification (see
// prepareProcessorActions: "all parts of a processor are updateable"), so
// every field, including Plugin, is diffed here as an ordinary update.
func diffProcessorFields(oldCfg, newCfg config.Processor) []string {
	var paths []string
	if oldCfg.Plugin != newCfg.Plugin {
		paths = append(paths, "plugin")
	}
	if oldCfg.Workers != newCfg.Workers {
		paths = append(paths, "workers")
	}
	if oldCfg.Condition != newCfg.Condition {
		paths = append(paths, "condition")
	}
	paths = append(paths, settingsDiffPaths(oldCfg.Settings, newCfg.Settings)...)
	return paths
}

// settingsDiffPaths returns "settings.<key>" for every key whose value
// differs (or that exists on only one side) between oldCfg and newCfg, sorted for
// deterministic output/hashing.
func settingsDiffPaths(oldCfg, newCfg map[string]string) []string {
	changed := make(map[string]struct{})
	for k, v := range oldCfg {
		if nv, ok := newCfg[k]; !ok || nv != v {
			changed[k] = struct{}{}
		}
	}
	for k, v := range newCfg {
		if ov, ok := oldCfg[k]; !ok || ov != v {
			changed[k] = struct{}{}
		}
	}
	if len(changed) == 0 {
		return nil
	}
	keys := make([]string, 0, len(changed))
	for k := range changed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	paths := make([]string, len(keys))
	for i, k := range keys {
		paths[i] = "settings." + k
	}
	return paths
}

func equalConnectorIDs(a, b []config.Connector) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

func equalProcessorIDs(a, b []config.Processor) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}
