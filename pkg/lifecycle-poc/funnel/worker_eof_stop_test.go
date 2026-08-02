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
	"io"
	"testing"

	"github.com/matryer/is"
	"go.uber.org/mock/gomock"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
)

// TestWorker_Do_EOFWhileStopping_IsGraceful covers the gap between
// doTaskAttempt's two graceful-exit branches.
//
// The io.EOF branch is gated on !w.stop.Load(), and its comment justifies that
// by saying the stop-armed case is "handled by the branch above". It is not:
// the branch above matches only context.Canceled and plugin.ErrPluginNotRunning.
// So a source that exhausts (io.EOF) at the same instant an external Stop arms
// w.stop matches NEITHER branch, falls through to the generic error path, and
// Worker.Do returns "failed to read from source: EOF". In the Service that
// error reaches rp.t.Kill, so a pipeline that stopped perfectly cleanly is
// reported as failed instead of UserStopped.
//
// This is a timing race in production (a source finishing exactly as an
// operator stops the pipeline) but is fully deterministic to test: arm the stop
// flag first, then have the source return io.EOF.
//
// Observed in CI as a flake in the N-source service tests — including one run
// where the pipeline hung for the full 10-minute test timeout rather than
// failing fast.
func TestWorker_Do_EOFWhileStopping_IsGraceful(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	sourceMock, tasks, taskNode := newMockTasks(ctrl, "sourceTask")
	dlq, _ := NewMockDLQ(ctrl, logger)

	worker, err := NewWorker(taskNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	// Worker.Do's loop is `for !w.stop.Load()`, so arming the flag up front
	// would skip the read entirely and prove nothing. The race being modelled
	// is a Stop landing WHILE the source read is in flight: the loop condition
	// was false when checked, and w.stop becomes true before doTaskAttempt
	// classifies the error. Setting the flag from inside the source's own Do is
	// exactly that interleaving, made deterministic.
	tasks[0].EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, *Batch) error {
			worker.stop.Store(true)
			return io.EOF
		}).AnyTimes()
	sourceMock.EXPECT().Teardown(gomock.Any()).Return(nil).AnyTimes()

	// A source exhausting is not a failure, whether or not something also
	// asked the worker to stop.
	is.NoErr(worker.Do(ctx))
}
