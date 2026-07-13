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
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/fsnotify/fsnotify"
	"github.com/matryer/is"
)

func TestResolveWatchTarget_Directory(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	watchDir, match, err := resolveWatchTarget(dir)
	is.NoErr(err)
	is.Equal(watchDir, dir)
	is.True(match("a.yaml"))
	is.True(match("a.YML"))
	is.True(!match("a.txt"))
	is.True(!match("a.yaml.bak"))
}

func TestResolveWatchTarget_SingleExistingFile(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "pipelines.yaml", validPipelineYAML)

	watchDir, match, err := resolveWatchTarget(path)
	is.NoErr(err)
	is.Equal(watchDir, dir)
	is.True(match("pipelines.yaml"))
	is.True(!match("other.yaml")) // single-file mode only matches that one name
}

func TestResolveWatchTarget_NotYetCreatedFile(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "brand-new.yaml")

	watchDir, match, err := resolveWatchTarget(path)
	is.NoErr(err)
	is.Equal(watchDir, dir)
	is.True(match("brand-new.yaml"))
}

func TestResolveWatchTarget_NeitherPathNorParentExists(t *testing.T) {
	is := is.New(t)
	_, _, err := resolveWatchTarget(filepath.Join(t.TempDir(), "missing-dir", "p.yaml"))
	is.True(err != nil)
}

func TestResolveWatchTarget_EmptyPath(t *testing.T) {
	is := is.New(t)
	_, _, err := resolveWatchTarget("")
	is.True(err != nil)
}

func TestWatcher_Relevant(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	w, err := New(&fakeProvisioner{}, &fakeLifecycle{}, nil, Options{Path: dir})
	is.NoErr(err)

	is.True(w.relevant(fsnotify.Event{Name: filepath.Join(dir, "a.yaml"), Op: fsnotify.Write}))
	is.True(w.relevant(fsnotify.Event{Name: filepath.Join(dir, "a.yaml"), Op: fsnotify.Create}))
	is.True(w.relevant(fsnotify.Event{Name: filepath.Join(dir, "a.yaml"), Op: fsnotify.Remove}))
	is.True(w.relevant(fsnotify.Event{Name: filepath.Join(dir, "a.yaml"), Op: fsnotify.Rename}))
	is.True(!w.relevant(fsnotify.Event{Name: filepath.Join(dir, "a.yaml"), Op: fsnotify.Chmod}))
	is.True(!w.relevant(fsnotify.Event{Name: filepath.Join(dir, "a.txt"), Op: fsnotify.Write}))
}

// TestConsume_EventToFileToPipeline drives the Watcher's core loop
// (consume) with synthetic fsnotify events over a real temp directory,
// proving the event->file->pipeline mapping end to end without needing a
// live fsnotify.Watcher: a Write event for a valid pipeline file results in
// exactly one ApplyPlanLive call for the pipeline ID that file defines.
func TestConsume_EventToFileToPipeline(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "orders.yaml", validPipelineYAML)

	applied := make(chan config.Pipeline, 1)
	prov := &fakeProvisioner{
		PlanFn: func(_ context.Context, desired config.Pipeline) (provisioning.Diff, error) {
			return nonEmptyDiff(desired.ID), nil
		},
		ApplyFn: func(_ context.Context, desired config.Pipeline, hash string, _ bool) (provisioning.Diff, error) {
			applied <- desired
			return provisioning.Diff{PipelineID: desired.ID, Hash: hash}, nil
		},
	}
	lc := &fakeLifecycle{}
	w, err := New(prov, lc, nil, Options{
		Path:     dir,
		Debounce: 10 * time.Millisecond,
	})
	is.NoErr(err)
	w.reporter = newReporter(&nopWriter{}, false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan fsnotify.Event, 1)
	errs := make(chan error)
	done := make(chan error, 1)
	go func() { done <- w.consume(ctx, events, errs) }()

	events <- fsnotify.Event{Name: path, Op: fsnotify.Write}

	select {
	case desired := <-applied:
		is.Equal(desired.ID, "orders")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the pipeline to be applied")
	}

	cancel()
	select {
	case err := <-done:
		is.True(err != nil) // consume returns ctx.Err() (context.Canceled) on shutdown
	case <-time.After(5 * time.Second):
		t.Fatal("consume did not return after context cancellation")
	}
}

// TestConsume_IrrelevantEvent_NeverApplies proves a non-matching file (wrong
// extension) never triggers a debouncer/apply at all.
func TestConsume_IrrelevantEvent_NeverApplies(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "notes.txt", "hello")

	prov := &fakeProvisioner{}
	w, err := New(prov, &fakeLifecycle{}, nil, Options{Path: dir, Debounce: 5 * time.Millisecond})
	is.NoErr(err)
	w.reporter = newReporter(&nopWriter{}, false)

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan fsnotify.Event, 1)
	errs := make(chan error)
	go func() { _ = w.consume(ctx, events, errs) }()

	events <- fsnotify.Event{Name: path, Op: fsnotify.Write}
	time.Sleep(50 * time.Millisecond)
	cancel()

	is.Equal(prov.applyCallCount(), 0)
}

// TestWatcher_Run_RealFsnotify exercises the real fsnotify.Watcher wiring
// (Run, not just consume) over a real temp directory: writing a valid
// pipeline file results in a real apply.
func TestWatcher_Run_RealFsnotify(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	applied := make(chan config.Pipeline, 1)
	prov := &fakeProvisioner{
		PlanFn: func(_ context.Context, desired config.Pipeline) (provisioning.Diff, error) {
			return nonEmptyDiff(desired.ID), nil
		},
		ApplyFn: func(_ context.Context, desired config.Pipeline, hash string, _ bool) (provisioning.Diff, error) {
			applied <- desired
			return provisioning.Diff{PipelineID: desired.ID, Hash: hash}, nil
		},
	}
	w, err := New(prov, &fakeLifecycle{}, nil, Options{
		Path:     dir,
		Debounce: 20 * time.Millisecond,
		Out:      &nopWriter{},
	})
	is.NoErr(err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	// Give the watcher a moment to register with the OS before writing —
	// otherwise the write could race the Add() call.
	time.Sleep(50 * time.Millisecond)
	is.NoErr(os.WriteFile(filepath.Join(dir, "orders.yaml"), []byte(validPipelineYAML), 0o600))

	select {
	case desired := <-applied:
		is.Equal(desired.ID, "orders")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a real fsnotify event to drive an apply")
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

type nopWriter struct{}

func (*nopWriter) Write(p []byte) (int, error) { return len(p), nil }
