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
	"testing"

	"github.com/gammazero/deque"
)

func BenchmarkNewTicketQueue(b *testing.B) {
	for i := 0; i < b.N; i++ {
		tq := NewTicketQueue[int, int]()
		defer tq.Close()
	}
	b.StopTimer() // don't measure Close
}

func BenchmarkTicketQueueTake(b *testing.B) {
	for _, N := range []int{1, 2, 8, 64, 128} {
		b.Run(fmt.Sprintf("TicketQueue-%d", N), func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			tq := NewTicketQueue[int, int]()
			go func() {
				for {
					_, _, err := tq.Next(ctx)
					if err == context.Canceled {
						return
					}
				}
			}()

			for i := 0; i < b.N; i++ {
				tickets := deque.Deque[Ticket[int, int]]{}
				for j := 0; j < N; j++ {
					ticket := tq.Take()
					tickets.PushBack(ticket)
				}
				for tickets.Len() > 0 {
					tq.Wait(ctx, tickets.PopFront())
				}
			}
			cancel()
		})
	}
}
