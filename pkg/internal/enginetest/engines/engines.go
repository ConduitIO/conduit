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

// Package engines assembles a pipeline of enginetest fakes on each of the two
// pipeline engines, so one test or benchmark body runs against both:
//
//   - V1 is pkg/lifecycle: the record-at-a-time node graph in
//     pkg/lifecycle/stream (source, source acker, fan-in, fan-out, destination,
//     destination acker, DLQ handler).
//   - V2 is pkg/lifecycle-poc (arch-v2): one funnel.Worker per source over a
//     shared destination sink, batch at a time.
//
// The drivers wire the same topology the services build for an N-source,
// M-destination pipeline without processors: every record from every source is
// written to every destination, and a source record is acked once every
// destination handled it (or the DLQ took it). They deliberately skip what is
// not under test here: the connector.Source/Destination wrappers, the position
// persister, pipeline status and recovery. What they exercise is the engine's
// ack propagation, ordering, stop and error handling.
package engines

import (
	"context"
	"sync"

	"github.com/conduitio/conduit/pkg/internal/enginetest"
)

// Topology is the pipeline a driver builds.
type Topology struct {
	// Sources are the N source fakes. At least one.
	Sources []*enginetest.Source
	// Destinations are the M destination fakes. At least one. Every record is
	// delivered to every destination.
	Destinations []*enginetest.Destination
	// DLQ returns the DLQ destination for a source. V1 has one DLQ for the whole
	// pipeline and calls it once with an empty source ID; V2 has one DLQ per
	// source and calls it with each source's ID. Nil means a fresh accept-all
	// destination each time. Use SingleDLQ to give a test one destination to
	// inspect.
	DLQ func(sourceID string) *enginetest.Destination
	// DLQWindow and DLQThreshold are the nack window size and the number of
	// nacks it tolerates, as in the pipeline DLQ config. A zero threshold is
	// the product default: a nack halts the pipeline. A zero window means the
	// default of 1; note that the engines read a window size of 0 as "no limit",
	// which accepts every nack into the DLQ.
	DLQWindow, DLQThreshold int
}

// SingleDLQ returns a DLQ func that always returns d. Valid for one source
// only on V2 (each source needs its own DLQ connector).
func SingleDLQ(d *enginetest.Destination) func(string) *enginetest.Destination {
	return func(string) *enginetest.Destination { return d }
}

func (t Topology) window() int {
	if t.DLQWindow <= 0 {
		return 1
	}
	return t.DLQWindow
}

func (t Topology) dlq(sourceID string) *enginetest.Destination {
	if t.DLQ != nil {
		return t.DLQ(sourceID)
	}
	return enginetest.NewDestination(enginetest.DestinationConfig{ID: "dlq-" + sourceID})
}

// Engine builds and starts a pipeline from a Topology.
type Engine interface {
	// Name is "v1" or "v2", for subtest names.
	Name() string
	// Start opens every connector and starts processing. It returns once the
	// pipeline is running, or the open error.
	Start(ctx context.Context, t Topology) (Run, error)
}

// Run is a started pipeline.
type Run interface {
	// Stop asks every source to stop gracefully, the way a user stop does:
	// in-flight records drain and are acked, then the pipeline exits.
	Stop(ctx context.Context) error
	// Wait blocks until the pipeline has exited and every connector is torn
	// down, and returns the error that ended it (nil after a clean stop). It
	// returns ctx.Err() if ctx ends first.
	Wait(ctx context.Context) error
	// Kill cancels the pipeline's context, the in-process equivalent of a
	// fatal error elsewhere. It does not wait.
	Kill()
}

// All returns both engines, for table-driven tests.
func All() []Engine { return []Engine{V1{}, V2{}} }

// runner is the shared Run implementation: a cancelable context, a set of
// goroutines and the first error among them.
type runner struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
	done   chan struct{}

	mu    sync.Mutex
	first error

	stop func(context.Context) error
}

func newRunner(cancel context.CancelFunc) *runner {
	return &runner{cancel: cancel, done: make(chan struct{})}
}

// fail records err if it is the first, and cancels the pipeline context so the
// other goroutines unwind, as the services do with their tomb.
func (r *runner) fail(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	if r.first == nil {
		r.first = err
	}
	r.mu.Unlock()
	r.cancel()
}

// start launches the goroutine that closes done once every goroutine added to
// wg has returned. Call after the last wg.Add.
func (r *runner) start(after func()) {
	go func() {
		r.wg.Wait()
		if after != nil {
			after()
		}
		close(r.done)
	}()
}

func (r *runner) Stop(ctx context.Context) error { return r.stop(ctx) }

func (r *runner) Kill() { r.cancel() }

func (r *runner) Wait(ctx context.Context) error {
	select {
	case <-r.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.first
}
