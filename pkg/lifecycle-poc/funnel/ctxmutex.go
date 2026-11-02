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

package funnel

import (
	"context"
	"sync"
	"sync/atomic"
)

// ctxMutex is a sync.Mutex that can also be acquired with a context. The stop path
// (Worker.Stop's drain) takes the shared-destination locks with the stop deadline
// so a sibling blocked behind a wedged destination cannot make Stop ignore it.
//
// The hot path (Lock/Unlock, taken for every batch through a shared destination)
// is a plain sync.Mutex. An earlier version was a one-slot channel semaphore; it
// doubled the time workers spent waiting on the shared-boundary lock in the
// in-process engine benchmark (-28% on 4x4/v2): a parked channel sender is woken
// by a hand-off, while a sync.Mutex waiter spins briefly and usually gets the lock
// without a goroutine switch.
//
// LockCtx does not poll TryLock (which can starve once the mutex is in starvation
// mode, where waiters are handed the lock directly). Its slow path parks one
// ordinary Lock() waiter in a helper goroutine, so it queues like any other
// waiter; if the context ends first, the helper releases the lock the moment it
// gets it. The zero value is ready to use.
type ctxMutex struct {
	mu sync.Mutex
}

// Lock acquires the mutex, waiting as long as it takes.
func (m *ctxMutex) Lock() { m.mu.Lock() }

// Unlock releases the mutex.
func (m *ctxMutex) Unlock() { m.mu.Unlock() }

const (
	lockPending   int32 = iota // helper still waiting for the mutex
	lockAcquired               // helper got it and handed it to the caller
	lockAbandoned              // the caller gave up; the helper must release it
)

// LockCtx acquires the mutex or returns ctx.Err() if ctx ends first. It returns
// nil only when the caller holds the lock.
func (m *ctxMutex) LockCtx(ctx context.Context) error {
	if m.mu.TryLock() {
		return nil
	}
	var state atomic.Int32
	acquired := make(chan struct{})
	go func() {
		m.mu.Lock()
		if state.CompareAndSwap(lockPending, lockAcquired) {
			close(acquired)
			return
		}
		m.mu.Unlock() // the caller gave up while we waited
	}()
	select {
	case <-acquired:
		return nil
	case <-ctx.Done():
		if state.CompareAndSwap(lockPending, lockAbandoned) {
			return ctx.Err()
		}
		<-acquired // the helper got the lock at the same instant: it is ours
		return nil
	}
}
