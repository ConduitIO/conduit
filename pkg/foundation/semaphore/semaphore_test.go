// Copyright © 2022 Meroxa, Inc.
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

package semaphore_test

import (
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/semaphore"
)

const maxSleep = 1 * time.Millisecond

func HammerSimple(sem *semaphore.Simple, loops int, mu *sync.Mutex) {
	for i := 0; i < loops; i++ {
		mu.Lock()
		tkn := sem.Enqueue()
		mu.Unlock()
		lock := sem.Acquire(tkn)
		//nolint:gosec // math/rand is good enough for a test
		time.Sleep(time.Duration(rand.Int63n(int64(maxSleep/time.Nanosecond))) * time.Nanosecond)
		sem.Release(lock)
	}
}

func TestSimple(t *testing.T) {
	t.Parallel()

	n := runtime.GOMAXPROCS(0)
	loops := 5000 / n
	sem := &semaphore.Simple{}

	var wg sync.WaitGroup
	wg.Add(n)
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			HammerSimple(sem, loops, &mu)
		}()
	}
	wg.Wait()
}

func TestSimpleReleaseTwice(t *testing.T) {
	t.Parallel()

	sem := &semaphore.Simple{}
	tkn := sem.Enqueue()
	lock := sem.Acquire(tkn)
	sem.Release(lock)

	defer func() {
		if recover() == nil {
			t.Fatal("expected to get a panic")
		}
	}()
	// release of an already released lock should panic
	sem.Release(lock)
}

func TestSimpleAcquire(t *testing.T) {
	t.Parallel()

	sem := &semaphore.Simple{}

	tkn1 := sem.Enqueue()
	lock := sem.Acquire(tkn1)

	tkn2done := make(chan struct{})
	go func() {
		defer close(tkn2done)
		tkn2 := sem.Enqueue()
		_ = sem.Acquire(tkn2) // don't release
	}()

	select {
	case <-tkn2done:
		t.Errorf("tkn2done closed prematurely")
	case <-time.After(time.Millisecond * 10):
		// tkn2 Acquire is blocking as expected
	}

	sem.Release(lock)

	select {
	case <-tkn2done:
		// tkn3 successfully acquired the semaphore
	case <-time.After(time.Millisecond * 10):
		t.Errorf("tkn2done didn't get closed")
	}
}

// TestLargeAcquireDoesntStarve times out if a large call to Acquire starves.
// Merely returning from the test function indicates success.
func TestLargeAcquireDoesntStarve(t *testing.T) {
	t.Parallel()

	n := int64(runtime.GOMAXPROCS(0))
	sem := &semaphore.Simple{}
	running := true

	var wg sync.WaitGroup
	wg.Add(int(n))
	var mu sync.Mutex
	for i := n; i > 0; i-- {
		mu.Lock()
		tkn := sem.Enqueue()
		mu.Unlock()
		lock := sem.Acquire(tkn)

		go func() {
			defer func() {
				sem.Release(lock)
				wg.Done()
			}()
			for running {
				time.Sleep(1 * time.Millisecond)
				sem.Release(lock)
				mu.Lock()
				tkn = sem.Enqueue()
				mu.Unlock()
				lock = sem.Acquire(tkn)
			}
		}()
	}

	mu.Lock()
	tkn := sem.Enqueue()
	mu.Unlock()
	lock := sem.Acquire(tkn)

	running = false
	sem.Release(lock)

	wg.Wait()
}

// ExampleSimple demonstrates how different goroutines can be orchestrated to
// acquire locks in the same order as the tickets enqueued in the semaphore.
func ExampleSimple_Enqueue() {
	var sem semaphore.Simple
	var wg sync.WaitGroup

	// t2 is enqueued after t1, it can be acquired only after t1
	t1 := sem.Enqueue()
	t2 := sem.Enqueue()
	wg.Add(2)

	go func() {
		defer wg.Done()
		time.Sleep(time.Millisecond) // simulate delay in acquiring
		fmt.Println("routine 1: try acquiring the lock")
		lock := sem.Acquire(t1)
		fmt.Println("routine 1: acquired the lock")
		sem.Release(lock)
	}()
	go func() {
		defer wg.Done()
		fmt.Println("routine 2: try acquiring the lock")
		lock := sem.Acquire(t2) // acquire will block because t1 needs to be acquired first
		fmt.Println("routine 2: acquired the lock")
		sem.Release(lock)
	}()

	wg.Wait()

	// Output:
	// routine 2: try acquiring the lock
	// routine 1: try acquiring the lock
	// routine 1: acquired the lock
	// routine 2: acquired the lock
}
