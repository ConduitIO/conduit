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
	"sync"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cchan"
)

// WaitGroup is a sync.WaitGroup with utility methods.
type WaitGroup sync.WaitGroup

// Add adds delta, which may be negative, to the WaitGroup counter.
// If the counter becomes zero, all goroutines blocked on Wait are released.
// If the counter goes negative, Add panics.
//
// Note that calls with a positive delta that occur when the counter is zero
// must happen before a Wait. Calls with a negative delta, or calls with a
// positive delta that start when the counter is greater than zero, may happen
// at any time.
// Typically this means the calls to Add should execute before the statement
// creating the goroutine or other event to be waited for.
// If a WaitGroup is reused to wait for several independent sets of events,
// new Add calls must happen after all previous Wait calls have returned.
// See the WaitGroup example.
func (wg *WaitGroup) Add(delta int) {
	(*sync.WaitGroup)(wg).Add(delta)
}

// Done decrements the WaitGroup counter by one.
func (wg *WaitGroup) Done() {
	(*sync.WaitGroup)(wg).Done()
}

// Wait blocks until the WaitGroup counter is zero. If the context gets canceled
// before that happens the method returns an error.
func (wg *WaitGroup) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		(*sync.WaitGroup)(wg).Wait()
		close(done)
	}()
	_, _, err := cchan.ChanOut[struct{}](done).Recv(ctx)
	return err
}

// WaitTimeout blocks until the WaitGroup counter is zero. If the context gets
// canceled or the timeout is reached before that happens the method returns an
// error.
func (wg *WaitGroup) WaitTimeout(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return wg.Wait(ctx)
}
