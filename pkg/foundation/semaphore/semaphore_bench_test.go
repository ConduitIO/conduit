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
	"fmt"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/semaphore"
)

func BenchmarkNewSem(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = &semaphore.Simple{}
	}
}

func BenchmarkAcquireSem(b *testing.B) {
	for _, N := range []int{1, 2, 8, 64, 128} {
		b.Run(fmt.Sprintf("acquire-%d", N), func(b *testing.B) {
			b.ResetTimer()
			sem := &semaphore.Simple{}
			for i := 0; i < b.N; i++ {
				for j := 0; j < N; j++ {
					t := sem.Enqueue()
					_ = sem.Acquire(t)
					_ = sem.Release(t)
				}
			}
		})
	}
}

func BenchmarkEnqueueReleaseSem(b *testing.B) {
	for _, N := range []int{1, 2, 8, 64, 128} {
		b.Run(fmt.Sprintf("enqueue/release-%d", N), func(b *testing.B) {
			b.ResetTimer()
			sem := &semaphore.Simple{}
			tickets := list.New()
			for i := 0; i < b.N; i++ {
				tickets.Init()
				for j := 0; j < N; j++ {
					t := sem.Enqueue()
					tickets.PushBack(t)
				}
				ticket := tickets.Front()
				for ticket != nil {
					_ = sem.Release(ticket.Value.(semaphore.Ticket))
					ticket = ticket.Next()
				}
			}
		})
	}
}
