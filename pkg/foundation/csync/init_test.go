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

func TestInit_WaitBeforeDone(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	var i Init

	var wg WaitGroup
	for j := 0; j < 100; j++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := i.Wait(ctx)
			is.NoErr(err)
		}()
	}

	// Init is not initialized, all goroutines should be waiting
	err := wg.WaitTimeout(ctx, time.Millisecond*10)
	is.Equal(err, context.DeadlineExceeded)

	// finish initialization and mark Init as done
	i.Done()

	// all gorotuines should be released now
	err = wg.WaitTimeout(ctx, time.Millisecond*10)
	is.NoErr(err)
}

func TestInit_DoneBeforeWait(t *testing.T) {
	is := is.New(t)
	var i Init

	// mark Init as done already
	i.Done()

	// try to wait for Init with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*10)
	defer cancel()

	err := i.Wait(ctx)
	is.NoErr(err)
}
