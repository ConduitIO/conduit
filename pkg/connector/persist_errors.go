// Copyright © 2026 Meroxa, Inc.
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

package connector

import (
	"sync"
)

// persistErrReporter delivers asynchronous persister failures from a
// PersistCallback to the node reading a connector's Errors() channel,
// without ever blocking the callback forever.
//
// Why it must not block: Persister runs every callback of a flush and closes
// that flush's callbacksDone only once all of them have returned. A callback
// stuck on an unbuffered errs channel nobody reads (after the node stopped
// reading, during teardown, after a failed Open, or under arch-v2 once
// funnel.Worker.WatchConnectorErrors has stopped, #2929) holds callbacksDone open, and every unbounded
// Persister.WaitPendingWrites caller (lifecycle StopAndWait, Persister.Wait
// at runtime shutdown) hangs with it (#2925).
//
// So send blocks on errs only until stop is called. stop is called when the
// connector starts tearing down and when Open fails after registering a
// persist; after it, send returns false immediately and the caller keeps the
// error itself.
type persistErrReporter struct {
	errs     chan error
	stopped  chan struct{}
	stopOnce sync.Once
}

func newPersistErrReporter(errs chan error) *persistErrReporter {
	return &persistErrReporter{errs: errs, stopped: make(chan struct{})}
}

// send delivers err on errs and returns true, or returns false without
// delivering if stop has been called (before or while waiting). After stop
// it never delivers, even if a reader happens to be waiting: once a
// connector is tearing down, its node may have stopped consuming whatever
// it receives.
func (r *persistErrReporter) send(err error) bool {
	select {
	case <-r.stopped:
		return false
	default:
	}
	select {
	case r.errs <- err:
		return true
	case <-r.stopped:
		return false
	}
}

// stop makes every pending and future send return false. Safe to call more
// than once and concurrently.
func (r *persistErrReporter) stop() {
	r.stopOnce.Do(func() { close(r.stopped) })
}
