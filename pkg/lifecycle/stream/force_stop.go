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

package stream

import (
	"context"
	"sync"
)

// forceStopper coordinates force-stopping a node's connector context with the
// node's Run initialization. A force-stoppable node holds one, calls start() when
// Run sets up its connector context, and calls stop() from ForceStop.
//
// The two can race: force-stop is gated only on the pipeline status being
// "running", which is set as the node goroutines launch, so ForceStop may run
// before Run has created the connector context. forceStopper makes that safe in
// both orderings:
//
//   - stop() after start(): cancels the connector context immediately.
//   - stop() before start(): latches the request; start() cancels the context the
//     moment it is created, so the force-stop is neither lost nor a nil-panic.
//
// All state is guarded by mu, so the cancel func written by start() (in the Run
// goroutine) and read by stop() (in the force-stop goroutine) never race.
//
// This exists because force-stop is a crash-safety guarantee (data-integrity
// invariant 7: force at any instant must be recoverable, not crash), and it was
// previously reimplemented per node — one of four correctly, the rest with a
// nil-deref panic and, for two, an unsynchronized field. See #2539.
type forceStopper struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	stopped bool
}

// start returns a fresh cancelable context for the node's connector and records
// its cancel func. If stop() already ran, the returned context is already
// canceled. The caller must defer the returned cancel func so the connector
// context is released when Run returns.
func (f *forceStopper) start() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancel = cancel
	if f.stopped {
		cancel()
	}
	return ctx, cancel
}

// stop cancels the connector context. If start() has not run yet, it latches the
// request so start() cancels immediately when it does.
func (f *forceStopper) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancel != nil {
		f.cancel()
		return
	}
	f.stopped = true
}
