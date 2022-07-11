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
	"fmt"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/semaphore"
)

func BenchmarkNewSem(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = &semaphore.Simple{}
	}
}

func BenchmarkEnqueueOneByOne(b *testing.B) {
	for _, N := range []int{1, 2, 8, 64, 128, 1024} {
		b.Run(fmt.Sprintf("ticket-count-%d", N), func(b *testing.B) {
			sem := &semaphore.Simple{}
			for i := 0; i < b.N; i++ {
				for j := 0; j < N; j++ {
					t := sem.Enqueue()
					sem.Acquire(t)
					_ = sem.Release(t)
				}
			}
		})
	}
}

func BenchmarkEnqueueAll(b *testing.B) {
	for _, N := range []int{1, 2, 8, 64, 128, 1024} {
		b.Run(fmt.Sprintf("ticket-count-%d", N), func(b *testing.B) {
			sem := &semaphore.Simple{}
			tickets := make([]semaphore.Ticket, N)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for j := 0; j < N; j++ {
					tickets[j] = sem.Enqueue()
				}
				for j := 0; j < N; j++ {
					sem.Acquire(tickets[j])
					_ = sem.Release(tickets[j])
				}
			}
		})
	}
}
