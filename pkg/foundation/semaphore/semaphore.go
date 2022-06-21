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

package semaphore

import (
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// Simple provides a way to bound concurrent access to a resource. It only
// allows one caller to gain access at a time.
type Simple struct {
	waiters  []waiter
	front    int
	batch    int64
	acquired bool
	released int
	mu       sync.Mutex
}

type waiter struct {
	index int
	ready chan struct{} // Closed when semaphore acquired.

	released bool
	acquired bool
}

// Ticket reserves a place in the queue and can be used to acquire access to a
// resource.
type Ticket struct {
	index int
	batch int64
}

// Enqueue reserves the next place in the queue and returns a Ticket used to
// acquire access to the resource when it's the callers turn. The Ticket has to
// be supplied to Release before discarding.
func (s *Simple) Enqueue() Ticket {
	s.mu.Lock()
	defer s.mu.Unlock()

	index := len(s.waiters)
	w := waiter{index: index, ready: make(chan struct{})}
	s.waiters = append(s.waiters, w)

	return Ticket{
		index: index,
		batch: s.batch,
	}
}

// Acquire acquires the semaphore, blocking until resources are available. On
// success, returns nil. On failure, returns an error and leaves the semaphore
// unchanged.
func (s *Simple) Acquire(t Ticket) error {
	s.mu.Lock()
	if s.batch != t.batch {
		s.mu.Unlock()
		return cerrors.Errorf("semaphore: invalid batch")
	}

	w := s.waiters[t.index]
	if w.acquired {
		return cerrors.New("semaphore: can't acquire ticket that was already acquired")
	}

	w.acquired = true // mark that Acquire was already called for this Ticket
	s.waiters[t.index] = w

	if s.front == t.index && !s.acquired {
		s.front++
		s.acquired = true
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	<-w.ready
	return nil
}

// Release releases the semaphore and notifies the next in line if any.
// If the ticket was already released the function returns an error. After the
// ticket is released it should be discarded.
func (s *Simple) Release(t Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.batch != t.batch {
		return cerrors.Errorf("semaphore: invalid batch")
	}
	w := s.waiters[t.index]
	if !w.acquired {
		return cerrors.New("semaphore: can't release ticket that was not acquired")
	}
	if w.released {
		return cerrors.New("semaphore: ticket already released")
	}

	w.released = true
	s.waiters[t.index] = w
	s.acquired = false
	s.released++
	s.notifyWaiter()
	if s.released == len(s.waiters) {
		s.increaseBatch()
	}
	return nil
}

func (s *Simple) notifyWaiter() {
	if len(s.waiters) > s.front {
		w := s.waiters[s.front]
		s.acquired = true
		s.front++
		close(w.ready)
	}
}

func (s *Simple) increaseBatch() {
	s.waiters = s.waiters[:0]
	s.batch++
	s.front = 0
	s.released = 0
}
