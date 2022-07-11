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
	"github.com/gammazero/deque"
)

// Simple provides a way to bound concurrent access to a resource. It only
// allows one caller to gain access at a time.
type Simple struct {
	tickets       deque.Deque[Ticket]
	index         int64
	acquiredIndex int64
	mu            sync.Mutex
}

// Ticket reserves a place in the queue and can be used to acquire access to a
// resource.
type Ticket struct {
	index    int64
	acquired chan struct{} // Closed when semaphore acquired.
}

// Enqueue reserves the next place in the queue and returns a Ticket used to
// acquire access to the resource when it's the callers turn. The Ticket has to
// be supplied to Release before discarding.
func (s *Simple) Enqueue() Ticket {
	s.mu.Lock()
	defer s.mu.Unlock()

	ticket := Ticket{index: s.index, acquired: make(chan struct{})}
	s.tickets.PushBack(ticket)
	s.index++

	return ticket
}

// Acquire acquires the semaphore, blocking until resources are available. On
// success, returns nil. On failure, returns an error and leaves the semaphore
// unchanged.
func (s *Simple) Acquire(t Ticket) error {
	s.mu.Lock()

	select {
	case <-t.acquired:
		s.mu.Unlock()
		return cerrors.New("semaphore: can't acquire ticket that was already acquired")
	default:
	}

	if s.acquiredIndex == t.index {
		s.tickets.PopFront()
		close(t.acquired)
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	<-t.acquired
	return nil
}

// Release releases the semaphore and notifies the next in line if any.
// If the ticket was already released the function returns an error. After the
// ticket is released it should be discarded.
func (s *Simple) Release(t Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	select {
	case <-t.acquired:
	default:
		return cerrors.New("semaphore: can't release ticket that was not acquired")
	}

	s.acquiredIndex++
	s.notifyWaiter()
	return nil
}

func (s *Simple) notifyWaiter() {
	if s.tickets.Len() > 0 {
		w := s.tickets.PopFront()
		close(w.acquired)
	}
}
