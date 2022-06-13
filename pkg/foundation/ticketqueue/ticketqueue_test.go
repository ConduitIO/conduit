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
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

func TestTicketQueue_Next_ContextCanceled(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*10)
	defer cancel()

	tq := NewTicketQueue[int, float64]()
	defer tq.Close()

	req, res, err := tq.Next(ctx)
	is.Equal(req, nil)
	is.Equal(res, nil)
	is.Equal(err, context.DeadlineExceeded)
}

func TestTicketQueue_Next_Closed(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	tq := NewTicketQueue[int, float64]()
	tq.Close() // close ticket queue

	req, res, err := tq.Next(ctx)
	is.Equal(req, nil)
	is.Equal(res, nil)
	is.True(err != nil)
}

func TestTicketQueue_Take_Closed(t *testing.T) {
	is := is.New(t)

	tq := NewTicketQueue[int, float64]()
	tq.Close() // close ticket queue, taking a ticket after this is not permitted

	defer func() {
		is.True(recover() != nil) // expected Take to panic
	}()

	tq.Take()
}

func TestTicketQueue_Wait_ContextCanceled(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*10)
	defer cancel()

	tq := NewTicketQueue[int, float64]()
	defer tq.Close()

	ticket := tq.Take()
	req, res, err := tq.Wait(ctx, ticket)
	is.Equal(req, nil)
	is.Equal(res, nil)
	is.Equal(err, context.DeadlineExceeded)
}

func TestTicketQueue_Wait_ReuseTicket(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	tq := NewTicketQueue[int, float64]()
	defer tq.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*10)
		defer cancel()
		_, _, err := tq.Next(ctx)
		is.NoErr(err)
		_, _, err = tq.Next(ctx)
		is.Equal(err, context.DeadlineExceeded)
	}()

	ticket := tq.Take()
	_, _, err := tq.Wait(ctx, ticket)
	is.NoErr(err)

	_, _, err = tq.Wait(ctx, ticket)
	is.True(err != nil) // expected error for ticket that was already cashed-in
	wg.Wait()
}

func TestTicketQueue_Next_NoTicketWaiting(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	tq := NewTicketQueue[int, float64]()
	defer tq.Close()

	tq.Take() // take ticket, but don't cash it in

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*10)
	defer cancel()
	_, _, err := tq.Next(ctx)
	is.Equal(err, context.DeadlineExceeded)
}

func TestTicketQueue_Take_Buffer(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	tq := NewTicketQueue[int, float64]()
	defer tq.Close()

	// TicketQueue supports an unbounded amount of tickets, keep taking tickets
	// for one second and take as many tickets as possible
	testDuration := time.Second

	var wg sync.WaitGroup
	var numTickets int
	start := time.Now()
	for time.Since(start) < testDuration {
		numTickets += 1
		ticket := tq.Take()
		go func() {
			defer wg.Done()
			_, _, err := tq.Wait(ctx, ticket)
			is.NoErr(err)
		}()
	}
	wg.Add(numTickets)
	t.Logf("took %d tickets in %s", numTickets, testDuration)

	for i := 0; i < numTickets; i++ {
		_, _, err := tq.Next(ctx)
		is.NoErr(err)
	}

	wg.Wait() // wait for all ticket goroutines to finish

	// try fetching next in line, but there is none
	ctx, cancel := context.WithTimeout(ctx, time.Millisecond*10)
	defer cancel()
	_, _, err := tq.Next(ctx)
	is.Equal(err, context.DeadlineExceeded)
}

func TestTicketQueue_HandOff(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	tq := NewTicketQueue[int, float64]()
	defer tq.Close()

	wantInt := 123
	wantFloat := 1.23

	done := make(chan struct{})
	go func() {
		defer close(done)
		ticket := tq.Take()
		req, res, err := tq.Wait(ctx, ticket)
		is.NoErr(err)
		req <- wantInt
		gotFloat := <-res
		is.Equal(wantFloat, gotFloat)
	}()

	req, res, err := tq.Next(ctx)
	is.NoErr(err)

	gotInt := <-req
	is.Equal(wantInt, gotInt)

	res <- wantFloat
	<-done
}

func ExampleTicketQueue() {
	ctx := context.Background()

	tq := NewTicketQueue[string, error]()
	defer tq.Close()

	sentence := []string{
		"Each", "word", "will", "be", "sent", "to", "the", "collector", "in",
		"a", "separate", "goroutine", "and", "even", "though", "they", "will",
		"sleep", "for", "a", "random", "amount", "of", "time,", "all", "words",
		"will", "be", "processed", "in", "the", "right", "order.",
	}

	r := rand.New(rand.NewSource(time.Now().UnixMilli()))
	var wg sync.WaitGroup
	for _, word := range sentence {
		t := tq.Take()
		wg.Add(1)
		go func(word string) {
			defer wg.Done()
			// sleep for a random amount of time to simulate work being done
			time.Sleep(time.Millisecond * time.Duration(r.Intn(100)))
			// try to cash in ticket
			req, res, err := tq.Wait(ctx, t)
			if err != nil {
				panic(cerrors.Errorf("unexpected error: %w", err))
			}
			req <- word // send word to collector
			err = <-res // receive error back
			if err != nil {
				panic(cerrors.Errorf("unexpected error: %w", err))
			}
		}(word)
	}

	// collect all tickets
	var builder strings.Builder
	for {
		ctx, cancel := context.WithTimeout(ctx, time.Millisecond*200)
		defer cancel()

		req, res, err := tq.Next(ctx)
		if err != nil {
			if err == context.DeadlineExceeded {
				break
			}
			panic(cerrors.Errorf("unexpected error: %w", err))
		}

		word := <-req
		_, err = builder.WriteRune(' ')
		if err != nil {
			res <- err
		}
		_, err = builder.WriteString(word)
		if err != nil {
			res <- err
		}
		close(res)
	}
	wg.Wait()

	fmt.Println(builder.String())

	// Output:
	// Each word will be sent to the collector in a separate goroutine and even though they will sleep for a random amount of time, all words will be processed in the right order.
}
