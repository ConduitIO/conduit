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

package funnel

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// connectorErrorsTask is implemented by tasks that wrap a connector which
// reports failures asynchronously on its Errors() channel: SourceTask and
// DestinationTask. Those channels carry what the connector cannot return from
// a call: a state write the persister could not commit (#2925), and a
// deferred source ack the plugin never accepted.
type connectorErrorsTask interface {
	connectorErrors() <-chan error
}

// WatchConnectorErrors reads the Errors() channel of every connector task
// reachable from w.FirstTask and calls fail with the first error received,
// wrapped with the connector's ID. fail is called at most once.
//
// The caller passes a fail that ends the whole run: in pkg/lifecycle-poc it
// kills the pipeline's tomb. Canceling a context local to Do would not be
// enough, because connector.Source.Read does not watch the context it is
// given; a blocked Read returns only once the plugin stream's context, which
// derives from the context the source was opened with, is canceled.
//
// The walk follows TaskNode.Next directly, so it includes the shared sink's
// destinations, which Tasks() stops before. With N workers sharing a
// destination, each of them reads that destination's channel; whichever
// receives an error fails the run.
//
// Readers stop when ctx is done or stop is called. stop waits for every
// reader to return, so after it returns nothing reads the channels. That is
// safe: a connector whose error is not read keeps it and returns it from
// Teardown (see connector.persistErrReporter). One goroutine per connector;
// once stop returns, each of them has returned from its read and has nothing
// left to do but exit.
func (w *Worker) WatchConnectorErrors(ctx context.Context, fail func(error)) (stop func()) {
	stop, _ = w.watchConnectorErrors(ctx, fail)
	return stop
}

// watchConnectorErrors is WatchConnectorErrors that also returns done, which
// is closed once every reader has returned, whichever of stop, ctx or a
// received error released it. stop waits on done. Production code has no use
// for done; tests use it to observe the ctx path, where nothing else signals
// that the readers are gone (#3035).
func (w *Worker) watchConnectorErrors(ctx context.Context, fail func(error)) (stop func(), done <-chan struct{}) {
	type reader struct {
		taskID string
		errs   <-chan error
	}
	var readers []reader
	seen := make(map[*TaskNode]bool)
	var walk func(*TaskNode)
	walk = func(n *TaskNode) {
		if seen[n] {
			return
		}
		seen[n] = true
		if t, ok := n.Task.(connectorErrorsTask); ok {
			if errs := t.connectorErrors(); errs != nil {
				readers = append(readers, reader{taskID: n.Task.ID(), errs: errs})
			}
		}
		for _, next := range n.Next {
			walk(next)
		}
	}
	walk(w.FirstTask)

	quit := make(chan struct{})
	allDone := make(chan struct{})
	var (
		remaining atomic.Int64
		once      sync.Once
	)
	// remaining is set before any reader starts, so it cannot reach zero
	// until the last of them returns.
	remaining.Store(int64(len(readers)))
	if len(readers) == 0 {
		close(allDone)
	}

	read := func(taskID string, errs <-chan error) {
		defer func() {
			if remaining.Add(-1) == 0 {
				close(allDone)
			}
		}()
		select {
		case <-quit:
		case <-ctx.Done():
		case err, ok := <-errs:
			if !ok || err == nil {
				return
			}
			once.Do(func() {
				fail(cerrors.Errorf("connector %s reported an error: %w", taskID, err))
			})
		}
	}
	for _, r := range readers {
		go read(r.taskID, r.errs)
	}

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() { close(quit) })
		<-allDone
	}, allDone
}
