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
	"context"
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
		err := sem.Acquire(context.Background(), tkn)
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

func TestWeightedPanicReleaseUnacquired(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("release of an unacquired weighted semaphore did not panic")
		}
	}()
	w := semaphore.NewWeighted(1)
	tkn := w.Enqueue(1)
	w.Release(tkn)
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

	ctx := context.Background()
	sem := semaphore.NewWeighted(2)
	tryAcquire := func(n int64) bool {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer cancel()

		tkn := sem.Enqueue(1)
		return sem.Acquire(ctx, tkn) == nil
	}

	tries := []bool{}
	tkn := sem.Enqueue(1)
	sem.Acquire(ctx, tkn)
	tries = append(tries, tryAcquire(1))
	tries = append(tries, tryAcquire(1))

	sem.Release(tkn)

	tkn = sem.Enqueue(1)
	sem.Acquire(ctx, tkn)
	tries = append(tries, tryAcquire(1))

	want := []bool{true, false, false}
	for i := range tries {
		if tries[i] != want[i] {
			t.Errorf("tries[%d]: got %t, want %t", i, tries[i], want[i])
		}
	}
}

// TestLargeAcquireDoesntStarve times out if a large call to Acquire starves.
// Merely returning from the test function indicates success.
func TestLargeAcquireDoesntStarve(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	n := int64(runtime.GOMAXPROCS(0))
	sem := semaphore.NewWeighted(n)
	running := true

	var wg sync.WaitGroup
	wg.Add(int(n))
	for i := n; i > 0; i-- {
		tkn := sem.Enqueue(1)
		sem.Acquire(ctx, tkn)
		go func() {
			defer func() {
				sem.Release(tkn)
				wg.Done()
			}()
			for running {
				time.Sleep(1 * time.Millisecond)
				sem.Release(tkn)
				tkn = sem.Enqueue(1)
				sem.Acquire(ctx, tkn)
			}
		}()
	}

	tkn := sem.Enqueue(n)
	sem.Acquire(ctx, tkn)
	running = false
	sem.Release(tkn)
	wg.Wait()
}

// translated from https://github.com/zhiqiangxu/util/blob/master/mutex/crwmutex_test.go#L43
func TestAllocCancelDoesntStarve(t *testing.T) {
	sem := semaphore.NewWeighted(10)

	// Block off a portion of the semaphore so that Acquire(_, 10) can eventually succeed.
	tkn := sem.Enqueue(1)
	sem.Acquire(context.Background(), tkn)

	// In the background, Acquire(_, 10).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		tkn := sem.Enqueue(10)
		sem.Acquire(ctx, tkn)
	}()

	// Wait until the Acquire(_, 10) call blocks.
	for {
		ctx, cancel := context.WithTimeout(ctx, time.Millisecond)
		tkn := sem.Enqueue(1)
		err := sem.Acquire(ctx, tkn)
		cancel()
		if err != nil {
			break
		}
		sem.Release(tkn)
		runtime.Gosched()
	}

	// Now try to grab a read lock, and simultaneously unblock the Acquire(_, 10) call.
	// Both Acquire calls should unblock and return, in either order.
	go cancel()

	tkn = sem.Enqueue(1)
	err := sem.Acquire(context.Background(), tkn)
	if err != nil {
		t.Fatalf("Acquire(_, 1) failed unexpectedly: %v", err)
	}
	sem.Release(tkn)
}
