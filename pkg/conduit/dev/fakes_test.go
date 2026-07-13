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
	"sync"
	"time"

	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/conduit/pkg/provisioning/config"
)

// fakeProvisioner is a test double for Provisioner: PlanFn/ApplyFn are
// called if set, otherwise Plan returns an empty Diff and ApplyPlanLive
// returns planResult unchanged. Every call is recorded for assertions.
type fakeProvisioner struct {
	mu sync.Mutex

	PlanFn  func(ctx context.Context, desired config.Pipeline) (provisioning.Diff, error)
	ApplyFn func(ctx context.Context, desired config.Pipeline, hash string, allowRestartOnRunning bool) (provisioning.Diff, error)

	planCalls  []config.Pipeline
	applyCalls []applyCall
}

type applyCall struct {
	desired               config.Pipeline
	hash                  string
	allowRestartOnRunning bool
}

func (f *fakeProvisioner) Plan(ctx context.Context, desired config.Pipeline) (provisioning.Diff, error) {
	f.mu.Lock()
	f.planCalls = append(f.planCalls, desired)
	f.mu.Unlock()
	if f.PlanFn != nil {
		return f.PlanFn(ctx, desired)
	}
	return provisioning.Diff{PipelineID: desired.ID}, nil
}

func (f *fakeProvisioner) ApplyPlanLive(ctx context.Context, desired config.Pipeline, hash string, allowRestartOnRunning bool) (provisioning.Diff, error) {
	f.mu.Lock()
	f.applyCalls = append(f.applyCalls, applyCall{desired: desired, hash: hash, allowRestartOnRunning: allowRestartOnRunning})
	f.mu.Unlock()
	if f.ApplyFn != nil {
		return f.ApplyFn(ctx, desired, hash, allowRestartOnRunning)
	}
	return provisioning.Diff{PipelineID: desired.ID, Hash: hash}, nil
}

func (f *fakeProvisioner) applyCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applyCalls)
}

// fakeLifecycle is a test double for LifecycleStarter.
type fakeLifecycle struct {
	mu sync.Mutex

	StartFn func(ctx context.Context, pipelineID string) error

	startCalls []string
}

func (f *fakeLifecycle) Start(ctx context.Context, pipelineID string) error {
	f.mu.Lock()
	f.startCalls = append(f.startCalls, pipelineID)
	f.mu.Unlock()
	if f.StartFn != nil {
		return f.StartFn(ctx, pipelineID)
	}
	return nil
}

func (f *fakeLifecycle) startCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.startCalls)
}

// fakeClock is a deterministic, manually-driven Clock for testing the
// debounce engine (debounce.go) without a real sleep. Every After call is
// recorded (in order) and reported on calls, so a test can synchronize
// ("wait until the debouncer actually asked for a timer") before firing one.
type fakeClock struct {
	mu      sync.Mutex
	pending []chan time.Time
	calls   chan time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{calls: make(chan time.Duration, 64)}
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.pending = append(c.pending, ch)
	c.mu.Unlock()
	c.calls <- d
	return ch
}

// awaitCall blocks until the next After call is recorded, or panics after 5s
// (a debouncer that never calls After is a bug the test must surface loudly,
// not hang forever).
func (c *fakeClock) awaitCall() time.Duration {
	select {
	case d := <-c.calls:
		return d
	case <-time.After(5 * time.Second):
		panic("fakeClock: timed out waiting for After to be called")
	}
}

// fireLatest fires the most recently created pending timer — the one a
// debouncer's single timerC variable currently references, since every new
// trigger replaces that reference with a fresh After call (see debounce.go).
// Any earlier, now-unreferenced timer left in pending is never observed by
// anything and firing it would be a no-op from the debouncer's perspective;
// fireLatest always targets the live one.
func (c *fakeClock) fireLatest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return
	}
	ch := c.pending[len(c.pending)-1]
	c.pending = c.pending[:len(c.pending)-1]
	ch <- time.Now()
}
