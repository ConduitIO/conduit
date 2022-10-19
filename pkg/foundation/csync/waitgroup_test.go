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

package csync

import (
	"context"
	"testing"
	"time"

	"github.com/matryer/is"
)

func TestWaitGroup_Wait_Empty(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	var wg WaitGroup
	wg.Add(1)
	wg.Done()
	err := wg.Wait(ctx)
	is.NoErr(err)
}

func TestWaitGroup_Wait_Canceled(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var wg WaitGroup
	wg.Add(1)

	err := wg.Wait(ctx)
	is.Equal(err, context.Canceled)
}

func TestWaitGroup_WaitTimeout_DeadlineReached(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	var wg WaitGroup
	wg.Add(1)

	start := time.Now()
	err := wg.WaitTimeout(ctx, time.Millisecond*100)
	since := time.Since(start)

	is.Equal(err, context.DeadlineExceeded)
	is.True(since >= time.Millisecond*100)
}
