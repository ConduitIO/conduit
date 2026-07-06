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
	"testing"

	"github.com/matryer/is"
)

func TestForceStopper_StopAfterStart(t *testing.T) {
	is := is.New(t)
	var fs forceStopper

	ctx, cancel := fs.start()
	defer cancel()
	is.NoErr(ctx.Err()) // running: not canceled yet

	fs.stop()
	is.True(ctx.Err() != nil) // stop cancels the connector context
}

// TestForceStopper_StopBeforeStart is the #2539 case: a force-stop that arrives
// before Run initialized the context must not be lost or panic — it latches, and
// start() returns an already-canceled context.
func TestForceStopper_StopBeforeStart(t *testing.T) {
	is := is.New(t)
	var fs forceStopper

	fs.stop() // no context yet — must latch, not panic

	ctx, cancel := fs.start()
	defer cancel()
	is.True(ctx.Err() != nil) // start applies the latched force-stop immediately
}

func TestForceStopper_StopIsIdempotent(t *testing.T) {
	is := is.New(t)
	var fs forceStopper

	fs.stop()
	fs.stop() // latched twice — no panic
	ctx, cancel := fs.start()
	defer cancel()
	is.True(ctx.Err() != nil)

	fs.stop() // after start — no panic
	fs.stop()
}

// TestForceStopper_ConcurrentStartStop proves the invariant that matters: no
// matter how start() (Run goroutine) and stop() (force-stop goroutine) interleave,
// the connector context always ends canceled — the force-stop is never lost — and
// the field access is race-free (run under -race).
func TestForceStopper_ConcurrentStartStop(t *testing.T) {
	is := is.New(t)

	for i := 0; i < 500; i++ {
		var fs forceStopper
		var (
			ctx    context.Context
			cancel context.CancelFunc
		)

		release := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-release
			ctx, cancel = fs.start()
		}()
		go func() {
			defer wg.Done()
			<-release
			fs.stop()
		}()
		close(release)
		wg.Wait()

		is.True(ctx.Err() != nil) // force-stop is honored regardless of ordering
		cancel()
	}
}
