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
	"container/list"
	"context"
	"sync"
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
	size    int64
	cur     int64
	mu      sync.Mutex
	waiters list.List
}

type waiter struct {
	acquired bool
	released bool
	n        int64
	ready    chan struct{} // Closed when semaphore acquired.
}

type Ticket struct {
	elem *list.Element
}

func (s *Weighted) Enqueue(n int64) Ticket {
	if n > s.size {
		panic("semaphore: tried to enqueue more than size of semaphore")
	}

	s.mu.Lock()
	w := waiter{n: n, ready: make(chan struct{})}
	e := s.waiters.PushBack(w)
	s.mu.Unlock()
	return Ticket{elem: e}
}

// Acquire acquires the semaphore with a weight of n, blocking until resources
// are available or ctx is done. On success, returns nil. On failure, returns
// ctx.Err() and leaves the semaphore unchanged.
//
// If ctx is already done, Acquire may still succeed without blocking.
func (s *Weighted) Acquire(ctx context.Context, t Ticket) error {
	w := t.elem.Value.(waiter)

	s.mu.Lock()
	if s.waiters.Front() == t.elem && s.size-s.cur >= w.n {
		s.cur += w.n
		s.waiters.Remove(t.elem)
		w.acquired = true
		t.elem.Value = w
		// If there are extra tokens left, notify other waiters.
		if s.size > s.cur {
			s.notifyWaiters()
		}
		s.mu.Unlock()
		return nil
	}
	if w.n > s.size {
		// Don't make other Acquire calls block on one that's doomed to fail.
		s.mu.Unlock()
		<-ctx.Done()
		return ctx.Err()
	}
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		err := ctx.Err()
		s.mu.Lock()
		select {
		case <-w.ready:
			// Acquired the semaphore after we were canceled.  Rather than trying to
			// fix up the queue, just pretend we didn't notice the cancelation.
			err = nil
		default:
			isFront := s.waiters.Front() == t.elem
			s.waiters.Remove(t.elem)
			// If we're at the front and there are extra tokens left, notify other waiters.
			if isFront && s.size > s.cur {
				s.notifyWaiters()
			}
		}
		s.mu.Unlock()
		return err

	case <-w.ready:
		return nil
	}
}

// Release releases the semaphore with a weight of n.
func (s *Weighted) Release(t Ticket) {
	w := t.elem.Value.(waiter)
	s.mu.Lock()
	if !w.acquired {
		s.mu.Unlock()
		panic("semaphore: can't release ticket that was not acquired")
	}
	if w.released {
		s.mu.Unlock()
		panic("semaphore: ticket released twice")
	}

	s.cur -= t.elem.Value.(waiter).n
	w.released = true
	t.elem.Value = w
	if s.cur < 0 {
		s.mu.Unlock()
		panic("semaphore: released more than held")
	}
	s.notifyWaiters()
	s.mu.Unlock()
}

func (s *Weighted) notifyWaiters() {
	for {
		next := s.waiters.Front()
		if next == nil {
			break // No more waiters blocked.
		}

		w := next.Value.(waiter)
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

		w.acquired = true
		next.Value = w
		s.cur += w.n
		s.waiters.Remove(next)
		close(w.ready)
	}
}
