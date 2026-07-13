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
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	json "github.com/goccy/go-json"
	"github.com/matryer/is"
)

const validPipelineYAML = `version: 2.2
pipelines:
  - id: orders
    status: running
    name: orders
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
      - id: dst
        type: destination
        plugin: builtin:log
`

const twoPipelinesOneInvalidYAML = `version: 2.2
pipelines:
  - id: good
    status: running
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
      - id: dst
        type: destination
        plugin: builtin:log
  - id: ""
    status: bogus-status
    connectors:
      - id: ""
        type: bogus-type
`

// newTestWatcher builds a Watcher wired to fakes, emitting --json events
// into a buffer for assertion via readEvents. dir is used as the (unused by
// these apply-level tests) watch root.
func newTestWatcher(t *testing.T, dir string, prov *fakeProvisioner, lc *fakeLifecycle, statusFn StatusFunc) (*Watcher, *bytes.Buffer) {
	t.Helper()
	is := is.New(t)
	var buf bytes.Buffer
	w, err := New(prov, lc, statusFn, Options{
		Path:   dir,
		Logger: log.Nop(),
		Out:    &buf,
		JSON:   true,
	})
	is.NoErr(err)
	return w, &buf
}

// readEvents parses every JSON line in buf into an Event.
func readEvents(t *testing.T, buf *bytes.Buffer) []Event {
	t.Helper()
	is := is.New(t)
	var events []Event
	sc := bufio.NewScanner(buf)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Event
		is.NoErr(json.Unmarshal(line, &e))
		events = append(events, e)
	}
	is.NoErr(sc.Err())
	return events
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	is := is.New(t)
	is.NoErr(os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestParseFile_Transient_EmptyFile(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "p.yaml", "")

	w, buf := newTestWatcher(t, dir, &fakeProvisioner{}, &fakeLifecycle{}, nil)
	pipelines, transient := w.parseFile(context.Background(), path)
	is.True(transient)
	is.Equal(len(pipelines), 0)
	is.Equal(buf.Len(), 0) // no error reported for a transient read
}

func TestParseFile_Transient_WhitespaceOnly(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "p.yaml", "   \n\t\n")

	w, buf := newTestWatcher(t, dir, &fakeProvisioner{}, &fakeLifecycle{}, nil)
	pipelines, transient := w.parseFile(context.Background(), path)
	is.True(transient)
	is.Equal(len(pipelines), 0)
	is.Equal(buf.Len(), 0)
}

func TestParseFile_SyntaxError_Reported(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "p.yaml", "not: [valid: yaml: at: all:\n  - -")

	w, buf := newTestWatcher(t, dir, &fakeProvisioner{}, &fakeLifecycle{}, nil)
	pipelines, transient := w.parseFile(context.Background(), path)
	is.True(!transient)
	is.Equal(len(pipelines), 0)

	events := readEvents(t, buf)
	is.True(len(events) >= 1)
	is.Equal(events[0].Outcome, OutcomeError)
}

func TestParseFile_ValidSinglePipeline(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "p.yaml", validPipelineYAML)

	w, buf := newTestWatcher(t, dir, &fakeProvisioner{}, &fakeLifecycle{}, nil)
	pipelines, transient := w.parseFile(context.Background(), path)
	is.True(!transient)
	is.Equal(len(pipelines), 1)
	is.Equal(pipelines[0].ID, "orders")
	is.Equal(buf.Len(), 0) // no errors for a valid file
}

// TestParseFile_OneBadDocumentDoesNotDropTheGoodOne matches
// provisioning.Service.parsePipelineConfigFile's rule (#2255): a single bad
// pipeline document in a multi-pipeline file must not prevent the valid
// ones from being applied.
func TestParseFile_OneBadDocumentDoesNotDropTheGoodOne(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "p.yaml", twoPipelinesOneInvalidYAML)

	w, buf := newTestWatcher(t, dir, &fakeProvisioner{}, &fakeLifecycle{}, nil)
	pipelines, transient := w.parseFile(context.Background(), path)
	is.True(!transient)
	is.Equal(len(pipelines), 1)
	is.Equal(pipelines[0].ID, "good")

	events := readEvents(t, buf)
	is.True(len(events) >= 1) // the invalid document was reported
	for _, e := range events {
		is.Equal(e.Outcome, OutcomeError)
	}
}

// TestApplyFile_RecoversFromPanic pins the invariant that a bad edit never
// crashes the server: applyFile runs on a plain goroutine (debounce.go), so a
// panic anywhere in the parse/enrich/validate/apply chain must be contained and
// reported as an error event, not propagated to kill the process (and with it
// every running pipeline).
func TestApplyFile_RecoversFromPanic(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "p.yaml", validPipelineYAML)
	prov := &fakeProvisioner{
		PlanFn: func(context.Context, config.Pipeline) (provisioning.Diff, error) { panic("boom in plan") },
	}
	w, buf := newTestWatcher(t, dir, prov, &fakeLifecycle{}, nil)

	// Must return normally (recovered), not propagate the panic.
	w.applyFile(context.Background(), path)

	events := readEvents(t, buf)
	is.True(len(events) >= 1)
	last := events[len(events)-1]
	is.Equal(last.Outcome, OutcomeError)
	is.True(last.Error != nil)
	is.True(strings.Contains(last.Error.Message, "panic")) // reported as a panic, not swallowed
}

func TestApplyPipeline_EmptyDiff_Skipped(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	prov := &fakeProvisioner{
		PlanFn: func(_ context.Context, desired config.Pipeline) (provisioning.Diff, error) {
			return provisioning.Diff{PipelineID: desired.ID}, nil // Empty() == true
		},
	}
	lc := &fakeLifecycle{}
	w, buf := newTestWatcher(t, dir, prov, lc, nil)

	desired := config.Enrich(config.Pipeline{ID: "orders"})
	w.applyPipeline(context.Background(), "p.yaml", desired)

	is.Equal(prov.applyCallCount(), 0) // ApplyPlanLive must never be called for an empty diff
	is.Equal(lc.startCallCount(), 0)

	events := readEvents(t, buf)
	is.Equal(len(events), 1)
	is.Equal(events[0].Outcome, OutcomeSkipped)
}

func nonEmptyDiff(id string) provisioning.Diff {
	return provisioning.Diff{
		PipelineID: id,
		Hash:       "deadbeef",
		Changes: []provisioning.Change{
			{Resource: provisioning.ResourceProcessor, ID: id + ":proc1", Action: provisioning.ChangeActionUpdate, Effect: provisioning.EffectInPlace, LiveSwappable: true},
		},
	}
}

func TestApplyPipeline_Mode_InPlace_WhenRunningAndLiveEligible(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	diff := nonEmptyDiff("orders")
	prov := &fakeProvisioner{PlanFn: func(_ context.Context, desired config.Pipeline) (provisioning.Diff, error) { return diff, nil }}
	lc := &fakeLifecycle{}
	statusFn := func(context.Context, string) (bool, error) { return true, nil }
	w, buf := newTestWatcher(t, dir, prov, lc, statusFn)

	desired := config.Enrich(config.Pipeline{ID: "orders"})
	w.applyPipeline(context.Background(), "p.yaml", desired)

	is.Equal(prov.applyCallCount(), 1)
	is.True(prov.applyCalls[0].allowRestartOnRunning) // the gate = the interactive invocation

	events := readEvents(t, buf)
	is.Equal(len(events), 1)
	is.Equal(events[0].Outcome, OutcomeApplied)
	is.Equal(events[0].Mode, ModeInPlace)
}

func TestApplyPipeline_Mode_Restart_WhenRunningAndNotLiveEligible(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	diff := provisioning.Diff{
		PipelineID: "orders",
		Hash:       "deadbeef",
		Changes: []provisioning.Change{
			{Resource: provisioning.ResourceConnector, ID: "orders:src", Action: provisioning.ChangeActionUpdate, Effect: provisioning.EffectInPlace, LiveSwappable: false},
		},
	}
	prov := &fakeProvisioner{PlanFn: func(_ context.Context, desired config.Pipeline) (provisioning.Diff, error) { return diff, nil }}
	lc := &fakeLifecycle{}
	statusFn := func(context.Context, string) (bool, error) { return true, nil }
	w, buf := newTestWatcher(t, dir, prov, lc, statusFn)

	desired := config.Enrich(config.Pipeline{ID: "orders"})
	w.applyPipeline(context.Background(), "p.yaml", desired)

	events := readEvents(t, buf)
	is.Equal(len(events), 1)
	is.Equal(events[0].Mode, ModeRestart)
}

func TestApplyPipeline_Mode_Provisioned_WhenNotRunning(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	diff := nonEmptyDiff("orders")
	prov := &fakeProvisioner{PlanFn: func(_ context.Context, desired config.Pipeline) (provisioning.Diff, error) { return diff, nil }}
	lc := &fakeLifecycle{}
	statusFn := func(context.Context, string) (bool, error) { return false, nil }
	w, buf := newTestWatcher(t, dir, prov, lc, statusFn)

	desired := config.Enrich(config.Pipeline{ID: "orders"}) // Status defaults to running
	w.applyPipeline(context.Background(), "p.yaml", desired)

	events := readEvents(t, buf)
	is.Equal(len(events), 1)
	is.Equal(events[0].Mode, ModeProvisioned)
	is.True(events[0].Started) // ensure-running started it
	is.Equal(lc.startCallCount(), 1)
}

// TestApplyPipeline_Mode_EngineOutcomeOverridesPlanGuess is the regression test
// for the mislabeled-mode bug: a processor edit produces a live-eligible plan,
// so the pre-apply guess is in_place — but the engine fell back to a restart
// (the processor could not be swapped live, e.g. it runs parallel), reported via
// Diff.AppliedMode. The event MUST report the engine's ground truth (restart),
// not the plan-derived guess (in_place). Without honoring AppliedMode, dev would
// tell the operator "applied in place, no restart" while the pipeline actually
// restarted.
func TestApplyPipeline_Mode_EngineOutcomeOverridesPlanGuess(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	planned := nonEmptyDiff("orders") // live-eligible: guess would be in_place
	is.True(planned.LiveEligible())
	applied := planned
	applied.AppliedMode = provisioning.ApplyModeRestart // engine actually fell back to a restart
	prov := &fakeProvisioner{
		PlanFn: func(_ context.Context, _ config.Pipeline) (provisioning.Diff, error) { return planned, nil },
		ApplyFn: func(_ context.Context, _ config.Pipeline, _ string, _ bool) (provisioning.Diff, error) {
			return applied, nil
		},
	}
	lc := &fakeLifecycle{}
	statusFn := func(context.Context, string) (bool, error) { return true, nil } // running -> guess=in_place
	w, buf := newTestWatcher(t, dir, prov, lc, statusFn)

	desired := config.Enrich(config.Pipeline{ID: "orders"})
	w.applyPipeline(context.Background(), "p.yaml", desired)

	events := readEvents(t, buf)
	is.Equal(len(events), 1)
	is.Equal(events[0].Mode, ModeRestart) // engine's AppliedMode wins over the in_place guess
}

// --- ensure-running ---

func TestEnsureRunning_NewFile_Starts(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	lc := &fakeLifecycle{}
	w, _ := newTestWatcher(t, dir, &fakeProvisioner{}, lc, nil)

	desired := config.Enrich(config.Pipeline{ID: "orders"}) // Status: running
	started := w.ensureRunning(context.Background(), desired)

	is.True(started)
	is.Equal(lc.startCallCount(), 1)
	is.Equal(lc.startCalls[0], "orders")
}

func TestEnsureRunning_PostFailureStopped_Starts(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	lc := &fakeLifecycle{} // Start succeeds — pipeline was left stopped by a prior failed apply
	w, _ := newTestWatcher(t, dir, &fakeProvisioner{}, lc, nil)

	desired := config.Enrich(config.Pipeline{ID: "orders"})
	started := w.ensureRunning(context.Background(), desired)

	is.True(started)
	is.Equal(lc.startCallCount(), 1)
}

func TestEnsureRunning_AlreadyRunning_NoErrorNoDoubleStart(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	lc := &fakeLifecycle{StartFn: func(context.Context, string) error { return pipeline.ErrPipelineRunning }}
	w, buf := newTestWatcher(t, dir, &fakeProvisioner{}, lc, nil)

	desired := config.Enrich(config.Pipeline{ID: "orders"})
	started := w.ensureRunning(context.Background(), desired)

	is.True(!started)      // already running: nothing to report as "started"
	is.Equal(buf.Len(), 0) // and definitely not an error
}

func TestEnsureRunning_StatusStopped_NeverStarts(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	lc := &fakeLifecycle{}
	w, _ := newTestWatcher(t, dir, &fakeProvisioner{}, lc, nil)

	desired := config.Enrich(config.Pipeline{ID: "orders", Status: config.StatusStopped})
	started := w.ensureRunning(context.Background(), desired)

	is.True(!started)
	is.Equal(lc.startCallCount(), 0) // config says stopped: dev must respect that
}

func TestEnsureRunning_StartFails_Reported(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	wantErr := cerrors.New("boom")
	lc := &fakeLifecycle{StartFn: func(context.Context, string) error { return wantErr }}
	w, buf := newTestWatcher(t, dir, &fakeProvisioner{}, lc, nil)

	desired := config.Enrich(config.Pipeline{ID: "orders"})
	started := w.ensureRunning(context.Background(), desired)

	is.True(!started)
	events := readEvents(t, buf)
	is.Equal(len(events), 1)
	is.Equal(events[0].Outcome, OutcomeError)
}

// --- file deletion ---

func TestHandleDeleted_KnownFile_ReportsAndLeavesRunning(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	prov := &fakeProvisioner{}
	lc := &fakeLifecycle{}
	w, buf := newTestWatcher(t, dir, prov, lc, nil)

	w.mu.Lock()
	w.filePipelines["orders.yaml"] = []string{"orders"}
	w.mu.Unlock()

	w.handleDeleted(context.Background(), "orders.yaml")

	// Never touches the pipeline: no Plan/Apply/Start call as a result of a
	// deletion.
	is.Equal(prov.applyCallCount(), 0)
	is.Equal(lc.startCallCount(), 0)

	events := readEvents(t, buf)
	is.Equal(len(events), 1)
	is.Equal(events[0].Outcome, OutcomeDeleted)
	is.Equal(events[0].PipelineID, "orders")

	// The path is forgotten so a later re-creation starts fresh.
	w.mu.Lock()
	_, tracked := w.filePipelines["orders.yaml"]
	w.mu.Unlock()
	is.True(!tracked)
}

func TestHandleDeleted_UnknownFile_NoEvent(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	w, buf := newTestWatcher(t, dir, &fakeProvisioner{}, &fakeLifecycle{}, nil)

	w.handleDeleted(context.Background(), "never-applied.yaml")

	is.Equal(buf.Len(), 0)
}

// --- applyFile end to end (in-process, no fsnotify) ---

func TestApplyFile_TracksPipelineIDsOnSuccess(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "orders.yaml", validPipelineYAML)

	diff := nonEmptyDiff("orders")
	prov := &fakeProvisioner{PlanFn: func(_ context.Context, desired config.Pipeline) (provisioning.Diff, error) { return diff, nil }}
	lc := &fakeLifecycle{}
	w, buf := newTestWatcher(t, dir, prov, lc, nil)

	w.applyFile(context.Background(), path)

	events := readEvents(t, buf)
	is.Equal(len(events), 1)
	is.Equal(events[0].Outcome, OutcomeApplied)
	is.Equal(events[0].PipelineID, "orders")

	w.mu.Lock()
	ids := w.filePipelines[path]
	w.mu.Unlock()
	is.Equal(len(ids), 1)
	is.Equal(ids[0], "orders")
}

func TestApplyFile_Deleted_LeavesTrackedPipelineRunning(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "orders.yaml") // never created

	prov := &fakeProvisioner{}
	lc := &fakeLifecycle{}
	w, buf := newTestWatcher(t, dir, prov, lc, nil)
	w.mu.Lock()
	w.filePipelines[path] = []string{"orders"}
	w.mu.Unlock()

	w.applyFile(context.Background(), path)

	is.Equal(prov.applyCallCount(), 0)
	is.Equal(lc.startCallCount(), 0)

	events := readEvents(t, buf)
	is.Equal(len(events), 1)
	is.Equal(events[0].Outcome, OutcomeDeleted)
}

func TestApplyFile_ParseError_NeverCallsApply(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "orders.yaml", "not: [valid: yaml:\n  -")

	prov := &fakeProvisioner{}
	lc := &fakeLifecycle{}
	w, buf := newTestWatcher(t, dir, prov, lc, nil)

	w.applyFile(context.Background(), path)

	is.Equal(prov.applyCallCount(), 0)
	is.Equal(lc.startCallCount(), 0)

	events := readEvents(t, buf)
	is.True(len(events) >= 1)
	is.Equal(events[0].Outcome, OutcomeError)
}
