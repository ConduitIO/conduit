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

// acquireN calls Acquire(size) on sem N times and then calls Release(size) N times.
func acquireN(b *testing.B, sem *semaphore.Simple, N int) {
	b.ResetTimer()
	tickets := list.New()
	for i := 0; i < b.N; i++ {
		tickets.Init()
		for j := 0; j < N; j++ {
			_ = sem.Enqueue()
		}
	}
}

func BenchmarkNewSem(b *testing.B) {
	for _, cap := range []int64{1, 128} {
		b.Run(fmt.Sprintf("Weighted-%d", cap), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = &semaphore.Simple{}
			}
		})
	}
}

func BenchmarkAcquireSem(b *testing.B) {
	for _, c := range []struct {
		cap int64
		N   int
	}{
		{1, 1},
		{2, 1},
		{16, 1},
		{128, 1},
		{2, 1},
		{16, 8},
		{128, 64},
		{2, 2},
		{16, 2},
		{128, 2},
	} {
		for _, w := range []struct {
			name string
			w    *semaphore.Simple
		}{
			{"Simple", &semaphore.Simple{}},
		} {
			b.Run(fmt.Sprintf("%s-acquire-%d-%d", w.name, c.cap, c.N), func(b *testing.B) {
				acquireN(b, w.w, c.N)
			})
		}
	}
}
