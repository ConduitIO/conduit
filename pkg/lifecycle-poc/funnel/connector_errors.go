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
// reader to exit, so after it returns nothing reads the channels. That is
// safe: a connector whose error is not read keeps it and returns it from
// Teardown (see connector.persistErrReporter). One goroutine per connector,
// all gone once stop returns.
func (w *Worker) WatchConnectorErrors(ctx context.Context, fail func(error)) (stop func()) {
	quit := make(chan struct{})
	var (
		wg   sync.WaitGroup
		once sync.Once
	)

	read := func(taskID string, errs <-chan error) {
		defer wg.Done()
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

	seen := make(map[*TaskNode]bool)
	var walk func(*TaskNode)
	walk = func(n *TaskNode) {
		if seen[n] {
			return
		}
		seen[n] = true
		if t, ok := n.Task.(connectorErrorsTask); ok {
			if errs := t.connectorErrors(); errs != nil {
				wg.Add(1)
				go read(n.Task.ID(), errs)
			}
		}
		for _, next := range n.Next {
			walk(next)
		}
	}
	walk(w.FirstTask)

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() { close(quit) })
		wg.Wait()
	}
}
