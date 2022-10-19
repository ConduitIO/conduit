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

package cchan

import (
	"context"
	"testing"
	"time"

	"github.com/matryer/is"
)

func TestChan_Recv_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	want := []int{1, 123, 1337}
	c := make(chan int, len(want))
	for _, w := range want {
		c <- w
	}

	for i := range want {
		got, ok, err := Chan[int](c).Recv(ctx)
		is.NoErr(err)
		is.True(ok)
		is.Equal(want[i], got)
	}
}

func TestChan_Recv_Closed(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	c := make(chan int)

	close(c)

	got, ok, err := Chan[int](c).Recv(ctx)
	is.NoErr(err)
	is.True(!ok)
	is.Equal(got, 0)
}

func TestChan_Recv_Canceled(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := make(chan int)
	got, ok, err := Chan[int](c).Recv(ctx)
	is.Equal(err, context.Canceled)
	is.True(!ok)
	is.Equal(got, 0)
}

func TestChan_RecvTimeout_DeadlineExceeded(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	c := make(chan int)
	start := time.Now()
	got, ok, err := Chan[int](c).RecvTimeout(ctx, time.Millisecond*100)
	since := time.Since(start)

	is.Equal(err, context.DeadlineExceeded)
	is.True(!ok)
	is.Equal(got, 0)

	is.True(since >= time.Millisecond*100)
}
