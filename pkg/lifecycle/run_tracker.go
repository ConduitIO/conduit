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

package lifecycle

import (
	"fmt"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
)

// runTracker counts the pipeline runs that are live and records whether the
// service has begun shutting down (#2901).
//
// A run is live from the moment runPipeline admits it, before any of its
// goroutines start, until its tomb is dead. Waiting on this count rather than
// on a snapshot of runningPipelines is what lets Wait guarantee that no run
// outlives it: a snapshot misses a run that a recovery restart publishes after
// the snapshot was taken, and a run whose nodes are running but which is not
// (or no longer) in the map.
//
// Once shutdown has begun, admit refuses every new run, so the count can only
// fall. That makes "count reached zero after shutdown began" final: nothing can
// start a run that writes positions after the runtime closes the database.
//
// The zero value is ready to use. All methods are safe for concurrent use.
type runTracker struct {
	mu   sync.Mutex
	live int
	// idle is closed while live == 0 and replaced when live goes 0 -> 1.
	// nil means "never admitted anything", which wait treats as idle.
	idle chan struct{}

	shutdown bool
	// reason is the reason StopAll was called with; see beginShutdown.
	reason error
	// done is closed when shutdown begins. Created lazily so the zero value
	// works.
	done chan struct{}
}

// admit registers a new live run. It returns false, and registers nothing,
// once shutdown has begun.
func (r *runTracker) admit() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shutdown {
		return false
	}
	if r.live == 0 {
		r.idle = make(chan struct{})
	}
	r.live++
	return true
}

// release unregisters a run admitted by admit. Call it exactly once per
// successful admit.
func (r *runTracker) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live--
	if r.live < 0 {
		panic("lifecycle: runTracker released more runs than it admitted")
	}
	if r.live == 0 {
		close(r.idle)
	}
}

// beginShutdown marks the service as shutting down with the given reason. It
// is idempotent: only the first call's reason is kept.
func (r *runTracker) beginShutdown(reason error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shutdown {
		return
	}
	r.shutdown = true
	r.reason = reason
	close(r.doneLocked())
}

// shuttingDown reports whether beginShutdown has been called, and its reason.
func (r *runTracker) shuttingDown() (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shutdown, r.reason
}

// shutdownStarted returns a channel that is closed once shutdown begins.
func (r *runTracker) shutdownStarted() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.doneLocked()
}

func (r *runTracker) doneLocked() chan struct{} {
	if r.done == nil {
		r.done = make(chan struct{})
	}
	return r.done
}

// wait blocks until no run is live.
//
// Without a shutdown in progress this returns at some instant where the count
// is zero; a run admitted afterwards is not waited for. After beginShutdown no
// run can be admitted, so a return is final.
func (r *runTracker) wait() {
	for {
		r.mu.Lock()
		if r.live == 0 {
			r.mu.Unlock()
			return
		}
		idle := r.idle
		r.mu.Unlock()
		<-idle
	}
}

// errShuttingDown is the error Start and runPipeline return once the service
// has begun shutting down. errors.Is(err, pipeline.ErrShuttingDown) holds.
func errShuttingDown(pipelineID string) error {
	err := conduiterr.Wrap(
		pipeline.CodeShuttingDown,
		fmt.Sprintf("can't start pipeline %s: %s", pipelineID, pipeline.ErrShuttingDown),
		pipeline.ErrShuttingDown,
	)
	err.Suggestion = "Conduit is shutting down and no longer starts pipelines; start the pipeline after Conduit has restarted"
	return err
}

// stopSignal records that a stop was requested for one pipeline run (#2901).
// It is fired by stopGraceful and stopForceful before they touch any node, and
// never reset: a run whose stop was requested never enters recovery and never
// gets restarted by a pending recovery. A restarted run is a new
// runnablePipeline with a fresh, unfired signal.
//
// The zero value is ready to use. All methods are safe for concurrent use.
type stopSignal struct {
	mu    sync.Mutex
	fired bool
	ch    chan struct{}
}

// fire records the stop request. Idempotent.
func (s *stopSignal) fire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fired {
		return
	}
	s.fired = true
	close(s.chLocked())
}

// requested reports whether fire has been called.
func (s *stopSignal) requested() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fired
}

// done returns a channel that is closed once fire has been called.
func (s *stopSignal) done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chLocked()
}

func (s *stopSignal) chLocked() chan struct{} {
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	return s.ch
}
