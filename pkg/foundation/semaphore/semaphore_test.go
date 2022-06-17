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
	"math/rand"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/semaphore"
)

const maxSleep = 1 * time.Millisecond

func HammerWeighted(sem *semaphore.Weighted, n int64, loops int) {
	for i := 0; i < loops; i++ {
		tkn := sem.Enqueue(n)
		err := sem.Acquire(tkn)
		if err != nil {
			panic(err)
		}
		time.Sleep(time.Duration(rand.Int63n(int64(maxSleep/time.Nanosecond))) * time.Nanosecond)
		sem.Release(tkn)
	}
}

func TestWeighted(t *testing.T) {
	t.Parallel()

	n := runtime.GOMAXPROCS(0)
	loops := 10000 / n
	sem := semaphore.NewWeighted(int64(n))

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			HammerWeighted(sem, int64(i), loops)
		}()
	}
	wg.Wait()
}

func TestWeightedReleaseUnacquired(t *testing.T) {
	t.Parallel()

	w := semaphore.NewWeighted(1)
	tkn := w.Enqueue(1)
	err := w.Release(tkn)
	if err == nil {
		t.Errorf("release of an unacquired ticket did not return an error")
	}
}

func TestWeightedReleaseTwice(t *testing.T) {
	t.Parallel()

	w := semaphore.NewWeighted(1)
	tkn := w.Enqueue(1)
	w.Acquire(tkn)
	err := w.Release(tkn)
	if err != nil {
		t.Errorf("release of an acquired ticket errored out: %v", err)
	}

	err = w.Release(tkn)
	if err == nil {
		t.Errorf("release of an already released ticket did not return an error")
	}
}

func TestWeightedAcquireTwice(t *testing.T) {
	t.Parallel()

	w := semaphore.NewWeighted(1)
	tkn := w.Enqueue(1)
	err := w.Acquire(tkn)
	if err != nil {
		t.Errorf("acquire of a ticket errored out: %v", err)
	}

	err = w.Acquire(tkn)
	if err == nil {
		t.Errorf("acquire of an already acquired ticket did not return an error")
	}
}

func TestWeightedPanicEnqueueTooBig(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("enqueue of size bigger than weighted semaphore did not panic")
		}
	}()
	const n = 5
	sem := semaphore.NewWeighted(n)
	sem.Enqueue(n + 1)
}

func TestWeightedAcquire(t *testing.T) {
	t.Parallel()

	sem := semaphore.NewWeighted(2)

	tkn1 := sem.Enqueue(1)
	sem.Acquire(tkn1)

	tkn2 := sem.Enqueue(1)
	sem.Acquire(tkn2)

	tkn3done := make(chan struct{})
	go func() {
		defer close(tkn3done)
		tkn3 := sem.Enqueue(1)
		sem.Acquire(tkn3)
	}()

	select {
	case <-tkn3done:
		t.Errorf("tkn3done closed prematurely")
	case <-time.After(time.Millisecond * 10):
		// tkn3 Acquire is blocking as expected
	}

	sem.Release(tkn1)

	select {
	case <-tkn3done:
		// tkn3 successfully acquired the semaphore
	case <-time.After(time.Millisecond * 10):
		t.Errorf("tkn3done didn't get closed")
	}
}

// TestLargeAcquireDoesntStarve times out if a large call to Acquire starves.
// Merely returning from the test function indicates success.
func TestLargeAcquireDoesntStarve(t *testing.T) {
	t.Parallel()

	n := int64(runtime.GOMAXPROCS(0))
	sem := semaphore.NewWeighted(n)
	running := true

	var wg sync.WaitGroup
	wg.Add(int(n))
	for i := n; i > 0; i-- {
		tkn := sem.Enqueue(1)
		sem.Acquire(tkn)
		go func() {
			defer func() {
				sem.Release(tkn)
				wg.Done()
			}()
			for running {
				time.Sleep(1 * time.Millisecond)
				sem.Release(tkn)
				tkn = sem.Enqueue(1)
				sem.Acquire(tkn)
			}
		}()
	}

	tkn := sem.Enqueue(n)
	sem.Acquire(tkn)
	running = false
	sem.Release(tkn)
	wg.Wait()
}
