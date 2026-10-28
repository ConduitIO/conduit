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
)

// ctxMutex is a mutual-exclusion lock that can also be acquired with a context.
// The stop path (Worker.Stop's drain) takes the shared-destination locks with the
// stop deadline so a sibling blocked behind a wedged destination cannot make Stop
// ignore it.
//
// It is a one-slot channel semaphore rather than a sync.Mutex polled with
// TryLock: a polling waiter can starve once the mutex is in starvation mode
// (blocking waiters are handed the lock directly), whereas a channel send waits
// in the runtime's queue like any other waiter. The zero value is ready to use.
type ctxMutex struct {
	once sync.Once
	sem  chan struct{}
}

func (m *ctxMutex) init() { m.once.Do(func() { m.sem = make(chan struct{}, 1) }) }

// Lock acquires the mutex, waiting as long as it takes.
func (m *ctxMutex) Lock() {
	m.init()
	m.sem <- struct{}{}
}

// LockCtx acquires the mutex or returns ctx.Err() if ctx ends first. It returns
// nil only when the caller holds the lock.
func (m *ctxMutex) LockCtx(ctx context.Context) error {
	m.init()
	select {
	case m.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Unlock releases the mutex. Like sync.Mutex, unlocking an unlocked mutex is a
// programming error.
func (m *ctxMutex) Unlock() {
	m.init()
	select {
	case <-m.sem:
	default:
		panic("funnel: unlock of unlocked ctxMutex")
	}
}
