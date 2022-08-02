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
)

// Simple provides a way to lock concurrent access to a resource. It only allows
// one caller to gain access at a time.
type Simple struct {
	// last holds the last issued lock.
	last Lock

	chanPool sync.Pool
	init     sync.Once
}

// Ticket reserves a place in the queue and can be used to acquire access to a
// Lock.
type Ticket struct {
	Lock
	// ready is the same channel as Lock.next of the previous lock. Once the
	// previous lock is released, the value i will be sent into ready, signaling
	// this ticket that it can acquire a Lock.
	ready chan int
}

// Lock represents a lock of the semaphore. After the lock is not needed any
// more it should be released.
type Lock struct {
	// i is the index of the current lock.
	i int
	// next is the same channel as Ticket.ready of the next ticket. Once Lock is
	// released, the value i+1 will be sent into next, signaling the next ticket
	// that it can acquire a Lock.
	next chan int
}

// Enqueue reserves the next place in the queue and returns a Ticket used to
// acquire access to the Lock when it's the callers turn. The Ticket has to be
// supplied to Acquire and discarded afterwards.
// Enqueue is not safe for concurrent use.
func (s *Simple) Enqueue() Ticket {
	s.init.Do(func() {
		s.chanPool.New = func() any {
			return make(chan int, 1) // make it buffered to not block the release of a lock
		}

		s.last.next = s.chanPool.Get().(chan int)
		s.last.next <- s.last.i + 1 // first lock can be acquired right away
	})

	t := Ticket{
		Lock: Lock{
			i:    s.last.i + 1,
			next: s.chanPool.Get().(chan int),
		},
		ready: s.last.next,
	}
	s.last = t.Lock
	return t
}

// Acquire acquires a Lock, blocking until the previous ticket is released.
// Ticket needs to be discarded after the call to Acquire. The Lock has to be
// supplied to Release after it is not needed anymore and discarded afterwards.
// Acquire is safe for concurrent use.
func (s *Simple) Acquire(t Ticket) Lock {
	i := <-t.ready
	if t.i != i {
		// Multiple reasons this can happen. A ticket other than this one could
		// have been successfully released twice. The other possibility is that
		// this ticket is trying to be acquired after it was released. Both
		// cases are invalid.
		s.panic()
	}
	s.chanPool.Put(t.ready)
	return t.Lock // return only the lock part of the ticket
}

// Release releases the lock and allows the next ticket in line to acquire a
// lock. After Lock is released it should be discarded.
// Release is safe for concurrent use.
func (s *Simple) Release(l Lock) {
	select {
	case l.next <- l.i + 1:
		// all good
	default:
		// This can happen if the ticket was already released and the next
		// ticket wasn't yet acquired.
		// In case the next ticket was already acquired, the operation will
		// succeed and the value will be pushed into the channel. Because the
		// channel is moved back to the pool, it is only a matter of time when
		// the channel is reused in another ticket that will see the wrong value
		// in the channel and panic.
		s.panic()
	}
}

func (s *Simple) panic() {
	panic("semaphore: mismatched ticket index, tickets are not supposed to" +
		" be acquired twice and should be discarded after they are released," +
		" this is an invalid use of this type")
}
