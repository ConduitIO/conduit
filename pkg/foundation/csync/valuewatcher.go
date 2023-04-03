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

	"github.com/google/uuid"
)

// ValueWatcher holds a reference to a value. Multiple goroutines are able to
// put or retrieve the value into/from the ValueWatcher, as well as wait for a
// certain value to be put into the ValueWatcher.
// It is similar to atomic.Value except the caller can call Watch to be notified
// each time the value in ValueWatcher changes.
type ValueWatcher[T any] struct {
	val       T
	m         sync.Mutex
	listeners map[string]chan T
}

type ValueWatcherFunc[T any] func(val T) bool

// WatchValues is a utility function for creating a simple ValueWatcherFunc that
// waits for one of the supplied values.
func WatchValues[T comparable](want ...T) ValueWatcherFunc[T] {
	if len(want) == 0 {
		// this would block forever, prevent misuse
		panic("invalid use of WatchValues, need to supply at least one value")
	}
	if len(want) == 1 {
		// optimize
		wantVal := want[0]
		return func(val T) bool {
			return val == wantVal
		}
	}
	return func(val T) bool {
		for _, wantVal := range want {
			if val == wantVal {
				return true
			}
		}
		return false
	}
}

// Set stores val in ValueWatcher and notifies all goroutines that called Watch
// about the new value, if such goroutines exists.
func (vw *ValueWatcher[T]) Set(val T) {
	vw.m.Lock()
	defer vw.m.Unlock()

	vw.val = val
	for _, l := range vw.listeners {
		l <- val
	}
}

// Get returns the current value stored in ValueWatcher.
func (vw *ValueWatcher[T]) Get() T {
	vw.m.Lock()
	defer vw.m.Unlock()

	return vw.val
}

// Watch blocks and calls f for every value that is put into the ValueWatcher.
// Once f returns true it stops blocking and returns nil. First call to f will
// be with the current value stored in ValueWatcher. Note that if no value was
// stored in ValueWatcher yet, the zero value of type T will be passed to f.
//
// Watch can be safely called by multiple goroutines. If the context gets
// cancelled before f returns true, the function will return the context error.
func (vw *ValueWatcher[T]) Watch(ctx context.Context, f ValueWatcherFunc[T]) (T, error) {
	val, found, listener, unsubscribe := vw.findOrSubscribe(f)
	if found {
		return val, nil
	}
	defer unsubscribe()

	// val was not found yet, we need to keep watching
	for {
		select {
		case <-ctx.Done():
			var empty T
			return empty, ctx.Err()
		case val = <-listener:
			if f(val) {
				return val, nil
			}
		}
	}
}

func (vw *ValueWatcher[T]) findOrSubscribe(f ValueWatcherFunc[T]) (T, bool, chan T, func()) {
	vw.m.Lock()
	defer vw.m.Unlock()

	// first call to f is with the current value
	if f(vw.val) {
		return vw.val, true, nil, nil
	}

	listener, unsubscribe := vw.subscribe()
	var empty T
	return empty, false, listener, unsubscribe
}

// subscribe creates a channel that will receive changes and returns it
// alongside a cleanup function that closes the channel and removes it from
// ValueWatcher.
func (vw *ValueWatcher[T]) subscribe() (chan T, func()) {
	if vw.listeners == nil {
		vw.listeners = make(map[string]chan T)
	}

	id := uuid.NewString()
	c := make(chan T)

	vw.listeners[id] = c

	return c, func() { vw.unsubscribe(id, c) }
}

func (vw *ValueWatcher[T]) unsubscribe(id string, c chan T) {
	// drain channel and remove it
	go func() {
		//nolint:revive // see comment below
		for range c {
			// Do nothing, just drain channel. In case another goroutine tries
			// to store a new value by calling ValueWatcher.Set, this goroutine
			// will unblock it until we successfully unsubscribe and remove the
			// channel from listeners.
		}
	}()

	vw.m.Lock()
	defer vw.m.Unlock()

	close(c)
	delete(vw.listeners, id)
}
