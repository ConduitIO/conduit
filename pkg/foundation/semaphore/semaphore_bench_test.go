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
	"container/list"
	"context"
	"fmt"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/semaphore"
)

// weighted is an interface matching a subset of *Weighted.  It allows
// alternate implementations for testing and benchmarking.
type weighted interface {
	Enqueue(int64) semaphore.Ticket
	Acquire(context.Context, semaphore.Ticket) error
	Release(semaphore.Ticket)
}

// acquireN calls Acquire(size) on sem N times and then calls Release(size) N times.
func acquireN(b *testing.B, sem weighted, size int64, N int) {
	b.ResetTimer()
	tickets := list.New()
	for i := 0; i < b.N; i++ {
		tickets.Init()
		for j := 0; j < N; j++ {
			ticket := sem.Enqueue(size)
			tickets.PushBack(ticket)
			sem.Acquire(context.Background(), ticket)
		}
		ticket := tickets.Front()
		for ticket != nil {
			sem.Release(ticket.Value.(semaphore.Ticket))
			ticket = ticket.Next()
		}
	}
}

func BenchmarkNewSeq(b *testing.B) {
	for _, cap := range []int64{1, 128} {
		b.Run(fmt.Sprintf("Weighted-%d", cap), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = semaphore.NewWeighted(cap)
			}
		})
	}
}

func BenchmarkAcquireSeq(b *testing.B) {
	for _, c := range []struct {
		cap, size int64
		N         int
	}{
		{1, 1, 1},
		{2, 1, 1},
		{16, 1, 1},
		{128, 1, 1},
		{2, 2, 1},
		{16, 2, 8},
		{128, 2, 64},
		{2, 1, 2},
		{16, 8, 2},
		{128, 64, 2},
	} {
		for _, w := range []struct {
			name string
			w    weighted
		}{
			{"Weighted", semaphore.NewWeighted(c.cap)},
		} {
			b.Run(fmt.Sprintf("%s-acquire-%d-%d-%d", w.name, c.cap, c.size, c.N), func(b *testing.B) {
				acquireN(b, w.w, c.size, c.N)
			})
		}
	}
}
