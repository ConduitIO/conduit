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
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/fsnotify/fsnotify"
)

// Options configures a Watcher.
type Options struct {
	// Path is the pipelines path to watch — a single .yml/.yaml file or a
	// directory of them (conduit.Config.Pipelines.Path; --pipelines.path).
	// Required.
	Path string

	// Debounce is the coalescing window (see debounce.go). Defaults to
	// DefaultDebounce (300ms) when zero.
	Debounce time.Duration

	// Clock abstracts time for the debounce engine. Defaults to the real
	// clock; tests inject a fake.
	Clock Clock

	// Logger receives diagnostic (debug/warn/error) logging. Required: the
	// zero value embeds a zero-value zerolog.Logger with a nil writer, which
	// panics on first use — pass log.Nop() explicitly to disable logging.
	Logger log.CtxLogger

	// Out is where Event output is written. Defaults to os.Stdout.
	Out io.Writer

	// JSON selects --json structured event lines over human-readable text.
	JSON bool
}

// Watcher watches Options.Path for pipeline config changes and drives
// Plan/ApplyPlanLive against them — see this package's doc for the full
// design.
type Watcher struct {
	provisioner Provisioner
	lifecycle   LifecycleStarter
	statusFn    StatusFunc

	watchDir string
	match    func(name string) bool

	debounce time.Duration
	clock    Clock
	logger   log.CtxLogger
	reporter *Reporter

	mu            sync.Mutex
	filePipelines map[string][]string // path -> pipeline IDs last successfully parsed from it
}

// New constructs a Watcher. provisioner and lifecycle are required;
// statusFn may be nil (see StatusFunc's doc for the effect). It resolves
// opts.Path immediately (stat-ing it, and its parent directory if it does
// not exist yet) so a bad --pipelines.path fails fast at startup rather than
// silently watching nothing.
func New(provisioner Provisioner, lifecycle LifecycleStarter, statusFn StatusFunc, opts Options) (*Watcher, error) {
	if provisioner == nil {
		return nil, cerrors.New("dev: a Provisioner is required")
	}
	if lifecycle == nil {
		return nil, cerrors.New("dev: a LifecycleStarter is required")
	}

	dir, match, err := resolveWatchTarget(opts.Path)
	if err != nil {
		return nil, err
	}

	debounce := opts.Debounce
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	return &Watcher{
		provisioner:   provisioner,
		lifecycle:     lifecycle,
		statusFn:      statusFn,
		watchDir:      dir,
		match:         match,
		debounce:      debounce,
		clock:         clock,
		logger:        opts.Logger.WithComponent("conduit.dev.Watcher"),
		reporter:      newReporter(out, opts.JSON),
		filePipelines: map[string][]string{},
	}, nil
}

// Run starts watching and blocks until ctx is cancelled, at which point it
// returns ctx.Err() (after every in-flight apply and per-path debouncer
// goroutine this call started has exited — see consume's doc). Invariant 7:
// callers (pkg/conduit.Runtime) derive ctx from the same serve context
// `conduit run`'s Ctrl-C/SIGTERM handling cancels.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return cerrors.Errorf("dev: could not start file watcher: %w", err)
	}
	defer fsw.Close() // best-effort cleanup on the way out

	if err := fsw.Add(w.watchDir); err != nil {
		return cerrors.Errorf("dev: could not watch %q: %w", w.watchDir, err)
	}

	w.logger.Info(ctx).
		Str("dir", w.watchDir).
		Dur("debounce", w.debounce).
		Msg("dev: watching for pipeline config changes")

	return w.consume(ctx, fsw.Events, fsw.Errors)
}

// consume is Run's core loop, split out so it can be unit-tested with
// synthetic events instead of a real fsnotify.Watcher (see watcher_test.go).
// It creates one debouncer per distinct file path the first time a relevant
// event names it, and waits for every debouncer goroutine it started to
// exit before returning — so Run never returns while an apply could still
// be in flight, and no goroutine leaks past a cancelled ctx.
func (w *Watcher) consume(ctx context.Context, events <-chan fsnotify.Event, fsErrors <-chan error) error {
	// Each debouncer goroutine (d.run) only exits when its context is cancelled.
	// Derive a child context and cancel it before wg.Wait so the debouncers are
	// always torn down no matter WHY consume returns — a parent-ctx cancel OR the
	// events/fsErrors channels closing (e.g. fsnotify.Close). Without this, a
	// channel-close return would leave d.run goroutines blocked forever and
	// wg.Wait would hang, stalling shutdown. Defer order matters: cancel() is
	// declared last so it runs first (LIFO), unblocking d.run before wg.Wait.
	ctx, cancel := context.WithCancel(ctx)
	debouncers := map[string]*debouncer{}
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if !w.relevant(ev) {
				continue
			}
			d, exists := debouncers[ev.Name]
			if !exists {
				path := ev.Name
				d = newDebouncer(w.clock, w.debounce, func(applyCtx context.Context) {
					w.applyFile(applyCtx, path)
				})
				debouncers[ev.Name] = d
				wg.Add(1)
				go func() {
					defer wg.Done()
					d.run(ctx)
				}()
			}
			d.trigger()

		case err, ok := <-fsErrors:
			if !ok {
				// The error channel is closed (the watcher is going away); stop
				// consuming rather than busy-spinning on a permanently-ready recv.
				return nil
			}
			w.logger.Err(ctx, err).Msg("dev: file watcher error")
		}
	}
}

// relevant reports whether ev is an fs event the Watcher should react to:
// one of the ops that can change a file's content or existence (Chmod-only
// events are noise — a permission change never changes what a pipeline
// config says), for a name this Watcher was configured to care about.
func (w *Watcher) relevant(ev fsnotify.Event) bool {
	const contentOps = fsnotify.Write | fsnotify.Create | fsnotify.Remove | fsnotify.Rename
	if ev.Op&contentOps == 0 {
		return false
	}
	return w.match(filepath.Base(ev.Name))
}

// resolveWatchTarget turns a conduit.Config.Pipelines.Path value (a single
// file or a directory) into the directory to hand fsnotify and a matcher
// for which file names within it are relevant.
//
// A single file is watched via its *parent directory* (not the file
// directly): watching an individual path is fragile across atomic-save
// editors, which replace the inode at that path via rename(2) — some
// platforms' fsnotify backends stop reporting events for a watch on the old,
// now-unlinked inode. Watching the directory and filtering by name (as the
// design doc's §4 "Debounce/coalesce" specifies) sidesteps this entirely and
// is what makes atomic-save tolerance possible in the first place.
func resolveWatchTarget(path string) (dir string, match func(name string) bool, err error) {
	if path == "" {
		return "", nil, cerrors.New("dev: pipelines path cannot be empty")
	}

	info, statErr := os.Stat(path)
	switch {
	case statErr == nil && info.IsDir():
		return path, hasYAMLExt, nil
	case statErr == nil:
		base := filepath.Base(path)
		return filepath.Dir(path), func(name string) bool { return name == base }, nil
	case os.IsNotExist(statErr):
		// path does not exist yet — most likely --pipelines.path naming a
		// not-yet-created file. Watch its parent directory (which must
		// exist) and match on this exact basename, so dev picks up the file
		// the moment it is created ("an empty dir is valid — dev still
		// watches so the first new file works", design doc §4).
		parent := filepath.Dir(path)
		if _, derr := os.Stat(parent); derr != nil {
			return "", nil, cerrors.Errorf("dev: pipelines path %q does not exist, and its parent directory %q is not accessible: %w", path, parent, derr)
		}
		base := filepath.Base(path)
		return parent, func(name string) bool { return name == base }, nil
	default:
		return "", nil, cerrors.Errorf("dev: could not stat pipelines path %q: %w", path, statErr)
	}
}

// hasYAMLExt reports whether name (a bare file name, not a full path) ends
// in .yml or .yaml, case-insensitively — the same rule
// pkg/provisioning/config.IsYAMLFile uses, minus the os.Stat call that
// function makes (which would always fail for a just-deleted file; this
// package needs to match a Remove event's name too).
func hasYAMLExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yml" || ext == ".yaml"
}
