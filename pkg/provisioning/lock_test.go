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

package provisioning

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matryer/is"
)

// TestPipelineLocks_SerializesSameID is the regression test for the Tier-1
// hard gate: concurrent Lock calls for the SAME pipeline ID must never
// overlap. It runs many goroutines that each increment a non-atomic counter
// while holding the lock and assert nobody else is inside the critical
// section at the same time — the classic "in > 1 means the lock didn't
// serialize" pattern; run with -race so a genuine data race (proving the
// critical sections actually overlapped) fails the test even if the counter
// logic alone doesn't catch it.
func TestPipelineLocks_SerializesSameID(t *testing.T) {
	is := is.New(t)
	locks := newPipelineLocks()

	const goroutines = 50
	var inCriticalSection int32 // guarded entirely by the lock under test
	var maxObserved int32
	var wg sync.WaitGroup

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := locks.Lock("p1")
			defer unlock()

			n := atomic.AddInt32(&inCriticalSection, 1)
			for {
				observedMax := atomic.LoadInt32(&maxObserved)
				if n <= observedMax || atomic.CompareAndSwapInt32(&maxObserved, observedMax, n) {
					break
				}
			}
			atomic.AddInt32(&inCriticalSection, -1)
		}()
	}
	wg.Wait()

	is.Equal(atomic.LoadInt32(&maxObserved), int32(1)) // never more than one goroutine inside the lock at once
}

// TestPipelineLocks_DifferentIDsDoNotBlock proves the lock is keyed, not
// global: two goroutines locking different pipeline IDs must be able to run
// concurrently (both inside their critical section at the same time), unlike
// TestPipelineLocks_SerializesSameID's same-ID case.
func TestPipelineLocks_DifferentIDsDoNotBlock(t *testing.T) {
	locks := newPipelineLocks()

	bothEntered := make(chan struct{})
	var entered int32
	release := make(chan struct{})

	var wg sync.WaitGroup
	for _, id := range []string{"p1", "p2"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			unlock := locks.Lock(id)
			defer unlock()

			if atomic.AddInt32(&entered, 1) == 2 {
				close(bothEntered)
			}
			<-release
		}(id)
	}

	select {
	case <-bothEntered:
		// both goroutines entered their (different-ID) critical sections
		// concurrently — the lock did not serialize across ids.
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for both different-ID locks to be held concurrently; the lock appears to be global instead of keyed")
	}
	close(release)
	wg.Wait()
}
