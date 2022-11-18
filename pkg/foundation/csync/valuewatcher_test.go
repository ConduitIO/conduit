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
	"testing"
	"time"

	"github.com/matryer/is"
	"go.uber.org/goleak"
)

func TestValueWatcher_GetEmptyValue(t *testing.T) {
	is := is.New(t)

	var h ValueWatcher[int]
	got := h.Get()
	is.Equal(0, got)
}

func TestValueWatcher_GetEmptyPtr(t *testing.T) {
	is := is.New(t)

	var h ValueWatcher[*int]
	got := h.Get()
	is.Equal(nil, got)
}

func TestValueWatcher_PutGetValue(t *testing.T) {
	is := is.New(t)

	var h ValueWatcher[int]
	want := 123
	h.Set(want)
	got := h.Get()
	is.Equal(want, got)
}

func TestValueWatcher_PutGetPtr(t *testing.T) {
	is := is.New(t)

	var h ValueWatcher[*int]
	want := 123
	h.Set(&want)
	got := h.Get()
	is.Equal(&want, got)
}

func TestValueWatcher_WatchSuccess(t *testing.T) {
	goleak.VerifyNone(t)
	is := is.New(t)

	var h ValueWatcher[int]

	putValue := make(chan int)
	defer close(putValue)
	go func() {
		for val := range putValue {
			h.Set(val)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	i := 0
	val, err := h.Watch(ctx, func(val int) bool {
		i++
		switch i {
		case 1:
			is.Equal(0, val) // expected first value to be 0
			putValue <- 123  // put next value
			return false     // not the value we are looking for
		case 2:
			is.Equal(123, val)
			putValue <- 555 // put next value
			return false    // not the value we are looking for
		case 3:
			is.Equal(555, val)
			return true // that's what we were looking for
		default:
			is.Fail() // unexpected value for i
			return false
		}
	})
	is.NoErr(err)
	is.Equal(3, i)
	is.Equal(555, val)

	got := h.Get()
	is.Equal(555, got)

	// we can still put more values into the watcher
	h.Set(666)
	got = h.Get()
	is.Equal(666, got)
}

func TestValueWatcher_WatchContextCancel(t *testing.T) {
	goleak.VerifyNone(t)
	is := is.New(t)

	var h ValueWatcher[int]
	h.Set(1)

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*10)
	defer cancel()

	i := 0
	val, err := h.Watch(ctx, func(val int) bool {
		i++
		is.Equal(1, val)
		return false
	})

	is.Equal(ctx.Err(), err)
	is.Equal(1, i)
	is.Equal(0, val)
}

func TestValueWatcher_WatchMultiple(t *testing.T) {
	const watcherCount = 100
	goleak.VerifyNone(t)
	is := is.New(t)

	var h ValueWatcher[int]

	// wg1 waits until all watchers are subscribed to changes
	var wg1 sync.WaitGroup
	// wg2 waits until all watchers found the value they were looking for
	var wg2 sync.WaitGroup

	wg1.Add(watcherCount)
	wg2.Add(watcherCount)
	for i := 0; i < watcherCount; i++ {
		go func(i int) {
			defer wg2.Done()
			var once sync.Once
			old := -1 // first call to Watch will be with 0, pretend old value was -1
			val, err := h.Watch(context.Background(), func(val int) bool {
				// first time the function is called with the current value in
				// ValueWatcher, after that we know the watcher is successfully
				// subscribed
				once.Do(wg1.Done)
				// make sure that we see all changes
				is.Equal(old+1, val)
				old = val
				// only stop when our specific number shows up
				return val == i
			})
			is.NoErr(err)
			is.Equal(val, i)
		}(i)
	}

	// wait for all watchers to be subscribed
	err := (*WaitGroup)(&wg1).WaitTimeout(context.Background(), time.Second)
	is.NoErr(err)

	// set the value incrementally higher
	for i := 1; i < watcherCount; i++ {
		h.Set(i)
	}

	// wait for all watchers to be done
	err = (*WaitGroup)(&wg2).WaitTimeout(context.Background(), time.Second)
	is.NoErr(err)
}

func TestValueWatcher_Concurrency(t *testing.T) {
	const watcherCount = 40
	const setterCount = 40
	const setCount = 20

	goleak.VerifyNone(t)
	is := is.New(t)

	var h ValueWatcher[int]

	// wg1 waits until all watchers are subscribed to changes
	var wg1 sync.WaitGroup
	// wg2 waits until all watchers found the value they were looking for
	var wg2 sync.WaitGroup

	wg1.Add(watcherCount)
	wg2.Add(watcherCount)
	for i := 0; i < watcherCount; i++ {
		go func(i int) {
			defer wg2.Done()
			var once sync.Once
			var count int
			_, err := h.Watch(context.Background(), func(val int) bool {
				once.Do(wg1.Done)
				count++
				// +1 because of first call
				return count == (setterCount*setCount)+1
			})
			is.NoErr(err)
			is.Equal(count, (setterCount*setCount)+1)
		}(i)
	}

	// wait for all watchers to be subscribed
	err := (*WaitGroup)(&wg1).WaitTimeout(context.Background(), time.Second)
	is.NoErr(err)

	// wg3 waits for all setters to stop setting values
	var wg3 sync.WaitGroup
	wg3.Add(setterCount)
	for i := 0; i < setterCount; i++ {
		go func(i int) {
			defer wg3.Done()
			for j := 0; j < setCount; j++ {
				h.Set(i)
			}
		}(i)
	}

	// wait for all setters to be done
	err = (*WaitGroup)(&wg3).WaitTimeout(context.Background(), time.Second)
	is.NoErr(err)

	// wait for all watchers to be done
	err = (*WaitGroup)(&wg2).WaitTimeout(context.Background(), time.Second)
	is.NoErr(err)
}
