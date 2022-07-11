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
	// lastTicket holds the last issued ticket.
	lastTicket Ticket
	// mu guards concurrent access to lastTicket.
	mu sync.Mutex
}

// Ticket reserves a place in the queue and can be used to acquire access to a
// resource.
type Ticket struct {
	// ready is closed when the ticket acquired the semaphore.
	ready chan struct{}
	// next is closed when the ticket is released.
	next chan struct{}
}

// Enqueue reserves the next place in the queue and returns a Ticket used to
// acquire access to the resource when it's the callers turn. The Ticket has to
// be supplied to Release before discarding.
func (s *Simple) Enqueue() Ticket {
	s.mu.Lock()
	defer s.mu.Unlock()

	t := Ticket{
		ready: s.lastTicket.next,
		next:  make(chan struct{}),
	}
	if t.ready == nil {
		// first time we create a ticket it will be already acquired
		t.ready = make(chan struct{})
		close(t.ready)
	}
	s.lastTicket = t
	return t
}

// Acquire acquires the semaphore, blocking until resources are available.
// Returns nil if acquire was successful or ctx.Err if the context was cancelled
// in the meantime.
func (s *Simple) Acquire(t Ticket) {
	<-t.ready
}

// Release releases the semaphore and notifies the next in line if any.
// If the ticket was already released the function returns an error. After the
// ticket is released it should be discarded.
func (s *Simple) Release(t Ticket) error {
	select {
	case <-t.ready:
	default:
		return cerrors.New("semaphore: can't release ticket that was not acquired")
	}

	select {
	case <-t.next:
		return cerrors.New("semaphore: ticket already released")
	default:
	}

	if t.next != nil {
		close(t.next)
	}
	return nil
}
