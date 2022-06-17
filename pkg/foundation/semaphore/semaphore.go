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

// Package semaphore provides a weighted semaphore implementation.
package semaphore

import (
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// NewWeighted creates a new weighted semaphore with the given
// maximum combined weight for concurrent access.
func NewWeighted(n int64) *Weighted {
	w := &Weighted{size: n}
	return w
}

// Weighted provides a way to bound concurrent access to a resource.
// The callers can request access with a given weight.
type Weighted struct {
	size     int64
	cur      int64
	released int
	mu       sync.Mutex

	waiters []waiter
	front   int
	batch   int64
}

type waiter struct {
	index int
	n     int64
	ready chan struct{} // Closed when semaphore acquired.

	released bool
	acquired bool
}

type Ticket struct {
	index int
	batch int64
}

func (s *Weighted) Enqueue(n int64) Ticket {
	if n > s.size {
		panic("semaphore: tried to enqueue more than size of semaphore")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	index := len(s.waiters)
	w := waiter{index: index, n: n, ready: make(chan struct{})}
	s.waiters = append(s.waiters, w)

	return Ticket{
		index: index,
		batch: s.batch,
	}
}

// Acquire acquires the semaphore with a weight of n, blocking until resources
// are available or ctx is done. On success, returns nil. On failure, returns
// ctx.Err() and leaves the semaphore unchanged.
//
// If ctx is already done, Acquire may still succeed without blocking.
func (s *Weighted) Acquire(t Ticket) error {
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

	if s.front == t.index && s.size-s.cur >= w.n {
		s.cur += w.n
		s.front++
		// If there are extra tokens left, notify other waiters.
		if s.size > s.cur {
			s.notifyWaiters()
		}
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	<-w.ready
	return nil
}

// Release releases the semaphore with a weight of n.
func (s *Weighted) Release(t Ticket) error {
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

	s.cur -= w.n
	w.released = true
	s.waiters[t.index] = w
	s.released++
	s.notifyWaiters()
	if s.released == len(s.waiters) {
		s.increaseBatch()
	}
	return nil
}

func (s *Weighted) notifyWaiters() {
	for len(s.waiters) > s.front {
		w := s.waiters[s.front]
		if s.size-s.cur < w.n {
			// Not enough tokens for the next waiter.  We could keep going (to try to
			// find a waiter with a smaller request), but under load that could cause
			// starvation for large requests; instead, we leave all remaining waiters
			// blocked.
			//
			// Consider a semaphore used as a read-write lock, with N tokens, N
			// readers, and one writer.  Each reader can Acquire(1) to obtain a read
			// lock.  The writer can Acquire(N) to obtain a write lock, excluding all
			// of the readers.  If we allow the readers to jump ahead in the queue,
			// the writer will starve — there is always one token available for every
			// reader.
			break
		}

		s.cur += w.n
		s.front++
		close(w.ready)
	}
}

func (s *Weighted) increaseBatch() {
	s.waiters = s.waiters[:0]
	s.batch += 1
	s.front = 0
}
