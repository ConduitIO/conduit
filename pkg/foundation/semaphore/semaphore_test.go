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

func HammerSimple(sem *semaphore.Simple, loops int) {
	for i := 0; i < loops; i++ {
		tkn := sem.Enqueue()
		err := sem.Acquire(tkn)
		if err != nil {
			panic(err)
		}
		//nolint:gosec // math/rand is good enough for a test
		time.Sleep(time.Duration(rand.Int63n(int64(maxSleep/time.Nanosecond))) * time.Nanosecond)
		err = sem.Release(tkn)
		if err != nil {
			panic(err)
		}
	}
}

func TestSimple(t *testing.T) {
	t.Parallel()

	n := runtime.GOMAXPROCS(0)
	loops := 5000 / n
	sem := &semaphore.Simple{}

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			HammerSimple(sem, loops)
		}()
	}
	wg.Wait()
}

func TestSimpleReleaseUnacquired(t *testing.T) {
	t.Parallel()

	w := &semaphore.Simple{}
	tkn := w.Enqueue()
	err := w.Release(tkn)
	if err == nil {
		t.Errorf("release of an unacquired ticket did not return an error")
	}
}

func TestSimpleReleaseTwice(t *testing.T) {
	t.Parallel()

	w := &semaphore.Simple{}
	tkn := w.Enqueue()
	err := w.Acquire(tkn)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	err = w.Release(tkn)
	if err != nil {
		t.Errorf("release of an acquired ticket errored out: %v", err)
	}

	err = w.Release(tkn)
	if err == nil {
		t.Errorf("release of an already released ticket did not return an error")
	}
}

func TestSimpleAcquireTwice(t *testing.T) {
	t.Parallel()

	w := &semaphore.Simple{}
	tkn := w.Enqueue()
	err := w.Acquire(tkn)
	if err != nil {
		t.Errorf("acquire of a ticket errored out: %v", err)
	}

	err = w.Acquire(tkn)
	if err == nil {
		t.Errorf("acquire of an already acquired ticket did not return an error")
	}
}

func TestSimpleAcquire(t *testing.T) {
	t.Parallel()

	sem := &semaphore.Simple{}

	tkn1 := sem.Enqueue()
	err := sem.Acquire(tkn1)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	tkn2done := make(chan struct{})
	go func() {
		defer close(tkn2done)
		tkn2 := sem.Enqueue()
		err := sem.Acquire(tkn2)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}()

	select {
	case <-tkn2done:
		t.Errorf("tkn2done closed prematurely")
	case <-time.After(time.Millisecond * 10):
		// tkn2 Acquire is blocking as expected
	}

	err = sem.Release(tkn1)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

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
	for i := n; i > 0; i-- {
		tkn := sem.Enqueue()
		err := sem.Acquire(tkn)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		go func() {
			defer func() {
				err := sem.Release(tkn)
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				wg.Done()
			}()
			for running {
				time.Sleep(1 * time.Millisecond)
				err := sem.Release(tkn)
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				tkn = sem.Enqueue()
				err = sem.Acquire(tkn)
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		}()
	}

	tkn := sem.Enqueue()
	err := sem.Acquire(tkn)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	running = false
	err = sem.Release(tkn)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	wg.Wait()
}
