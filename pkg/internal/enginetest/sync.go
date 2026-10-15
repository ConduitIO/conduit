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

package enginetest

import (
	"context"
	"sync"
	"time"
)

// Gate is a switch a test uses to stall a fake and release it again. The zero
// value is open. While the gate is closed, Wait blocks.
type Gate struct {
	mu sync.Mutex
	ch chan struct{} // nil when open; closed (and set to nil) on Open
}

// Close makes subsequent Wait calls block until Open. Closing a closed gate is
// a no-op.
func (g *Gate) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ch == nil {
		g.ch = make(chan struct{})
	}
}

// Open releases every goroutine blocked in Wait.
func (g *Gate) Open() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ch != nil {
		close(g.ch)
		g.ch = nil
	}
}

// Wait returns nil once the gate is open, or ctx.Err() if ctx ends first. done
// is an optional second release channel (the fakes pass their teardown channel
// so a stalled call never outlives its connector); when it closes Wait returns
// errGateAbandoned.
func (g *Gate) Wait(ctx context.Context, done <-chan struct{}) error {
	g.mu.Lock()
	ch := g.ch
	g.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return errGateAbandoned
	}
}

// counter is an integer a test can wait on without polling.
type counter struct {
	mu   sync.Mutex
	n    int
	wake chan struct{} // closed and replaced on every change
}

func (c *counter) add(delta int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n += delta
	if c.wake != nil {
		close(c.wake)
		c.wake = nil
	}
}

func (c *counter) load() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// waitAtLeast blocks until the counter reaches n or ctx ends.
func (c *counter) waitAtLeast(ctx context.Context, n int) error {
	for {
		c.mu.Lock()
		if c.n >= n {
			c.mu.Unlock()
			return nil
		}
		if c.wake == nil {
			c.wake = make(chan struct{})
		}
		wake := c.wake
		c.mu.Unlock()

		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// sleep waits for d, or until ctx or done ends. A zero d returns immediately
// without touching a timer, so the zero-latency path stays allocation free.
func sleep(ctx context.Context, d time.Duration, done <-chan struct{}) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return errGateAbandoned
	}
}

// broadcast wakes every goroutine currently waiting on C.
type broadcast struct {
	mu sync.Mutex
	ch chan struct{}
}

// C returns a channel that is closed by the next Fire.
func (b *broadcast) C() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch == nil {
		b.ch = make(chan struct{})
	}
	return b.ch
}

// Fire releases everything waiting on the current channel.
func (b *broadcast) Fire() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch != nil {
		close(b.ch)
		b.ch = nil
	}
}

// withDone returns a context that ends when ctx ends or done closes, so a hook
// a test left blocked is released when the connector is torn down.
func withDone(ctx context.Context, done <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
