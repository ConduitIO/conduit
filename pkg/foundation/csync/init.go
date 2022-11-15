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

	"github.com/conduitio/conduit/pkg/foundation/cchan"
)

// Init waits for a main goroutine to finish initialization before releasing
// other goroutines.
type Init struct {
	initOnce sync.Once
	done     chan struct{}
}

func (i *Init) init() {
	i.initOnce.Do(func() {
		i.done = make(chan struct{})
	})
}

// Done should be called by the goroutine responsible for initializing. Once
// this call finishes any other goroutines calling Wait will be released. Done
// can be called multiple times, only the first call will have an effect.
// This function is not safe for concurrent use.
func (i *Init) Done() {
	i.init()
	select {
	case <-i.done:
		// i.done is closed, all good
	default:
		// close i.done if it's still open
		close(i.done)
	}
}

// Wait can be called by a gorotuine to wait for another goroutine to finish
// initializing the object. This function is safe for concurrent use.
func (i *Init) Wait(ctx context.Context) error {
	i.init()
	_, _, err := cchan.Chan[struct{}](i.done).Recv(ctx)
	return err
}
