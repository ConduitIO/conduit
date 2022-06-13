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

package ticketqueue

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/gammazero/deque"
)

// TicketQueue dispenses tickets and keeps track of their order. Tickets can be
// "cashed-in" for channels that let the caller communicate with a worker that
// is supposed to handle the ticket. TicketQueue ensures that tickets are
// cashed-in in the exact same order as they were dispensed.
//
// Essentially TicketQueue simulates a "take a number" system where the number
// on the ticket is monotonically increasing with each dispensed ticket. When
// the monitor displays the number on the ticket, the person holding the ticket
// can approach the counter.
//
// TicketQueue contains an unbounded buffer for tickets. A goroutine is pushing
// tickets to workers calling Next. To stop this goroutine the TicketQueue needs
// to be closed and all tickets need to be drained through Next until it returns
// an error.
type TicketQueue[REQ, RES any] struct {
	// in is the channel where incoming tickets are sent into (see Take)
	in chan Ticket[REQ, RES]
	// out is the channel where outgoing tickets are sent into (see Next)
	out chan Ticket[REQ, RES]
}

// NewTicketQueue returns an initialized TicketQueue.
func NewTicketQueue[REQ, RES any]() *TicketQueue[REQ, RES] {
	tq := &TicketQueue[REQ, RES]{
		in:  make(chan Ticket[REQ, RES]),
		out: make(chan Ticket[REQ, RES]),
	}
	tq.run()
	return tq
}

// Ticket is dispensed by TicketQueue. Once TicketQueue.Wait is called with a
// Ticket it should be discarded.
type Ticket[REQ, RES any] struct {
	ctrl chan struct{}
	req  chan REQ
	res  chan RES
}

// run launches a goroutine that fetches tickets from the channel in and buffers
// them in an unbounded queue. It also pushes tickets from the queue into the
// channel out.
func (tq *TicketQueue[REQ, RES]) run() {
	in := tq.in

	// Deque is used as a normal queue and holds references to all open tickets
	var q deque.Deque[Ticket[REQ, RES]]
	outOrNil := func() chan Ticket[REQ, RES] {
		if q.Len() == 0 {
			return nil
		}
		return tq.out
	}
	nextTicket := func() Ticket[REQ, RES] {
		if q.Len() == 0 {
			return Ticket[REQ, RES]{}
		}
		return q.Front()
	}

	go func() {
		defer close(tq.out)
		for q.Len() > 0 || in != nil {
			select {
			case v, ok := <-in:
				if !ok {
					in = nil
					continue
				}
				q.PushBack(v)
			case outOrNil() <- nextTicket():
				q.PopFront() // remove ticket from queue
			}
		}
	}()
}

// Take creates a ticket. The ticket can be used to call Wait. If TicketQueue
// is already closed, the call panics.
func (tq *TicketQueue[REQ, RES]) Take() Ticket[REQ, RES] {
	t := Ticket[REQ, RES]{
		ctrl: make(chan struct{}),
		req:  make(chan REQ),
		res:  make(chan RES),
	}
	tq.in <- t
	return t
}

// Wait will block until all tickets before this ticket were already processed.
// Essentially this method means the caller wants to enqueue and wait for their
// turn. The function returns two channels that can be used to communicate with
// the processor of the ticket. The caller determines what messages are sent
// through those channels (if any). After Wait returns the ticket should be
// discarded.
//
// If ctx gets cancelled before the ticket is redeemed, the function returns the
// context error. If Wait is called a second time with the same ticket, the call
// returns an error.
func (tq *TicketQueue[REQ, RES]) Wait(ctx context.Context, t Ticket[REQ, RES]) (chan<- REQ, <-chan RES, error) {
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case _, ok := <-t.ctrl:
		if !ok {
			return nil, nil, cerrors.New("ticket already used")
		}
	}
	return t.req, t.res, nil
}

// Next can be used to fetch the channels to communicate with the next ticket
// holder in line. If there is no next ticket holder or if the next ticket
// holder did not call Wait, the call will block.
//
// If ctx gets cancelled before the next ticket holder is ready, the function
// returns the context error. If TicketQueue is closed and there are no more
// open tickets, the call returns an error.
func (tq *TicketQueue[REQ, RES]) Next(ctx context.Context) (<-chan REQ, chan<- RES, error) {
	var t Ticket[REQ, RES]
	var ok bool

	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case t, ok = <-tq.out:
		if !ok {
			return nil, nil, cerrors.New("TicketQueue is closed")
		}
	}

	select {
	case <-ctx.Done():
		// BUG: the ticket is lost at this point
		return nil, nil, ctx.Err()
	case t.ctrl <- struct{}{}: // signal that Next is ready to proceed
		close(t.ctrl) // ticket is used
	}

	return t.req, t.res, nil
}

// Close the ticket queue, no more new tickets can be dispensed after this.
// Calls to Wait and Next are still allowed until all open tickets are redeemed.
func (tq *TicketQueue[REQ, RES]) Close() {
	close(tq.in)
}
