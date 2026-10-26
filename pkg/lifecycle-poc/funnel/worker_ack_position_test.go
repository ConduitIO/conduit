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
	"testing"

	"github.com/matryer/is"
	"go.uber.org/mock/gomock"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
)

// TestWorker_Ack_EmptyPosition_RefusesToWipeSourcePosition is the Ack twin of
// TestWorker_Nack_TailOnlySubBatch_RefusesToWipeSourcePosition. guardmut found
// that deleting the guard in Worker.Ack left every test green: the Nack path
// was tested, the Ack path was not (invariant 2).
func TestWorker_Ack_EmptyPosition_RefusesToWipeSourcePosition(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)
	ctrl := gomock.NewController(t)

	sourceMock, _, taskNode := newMockTasks(ctrl, "sourceTask", "task1")
	dlq, _ := NewMockDLQ(ctrl, logger)

	batch := randomBatch(3)
	batch.SplitRecord(1, randomRecords(3)) // positions: p0, p1, nil, nil, p2
	tail := batch.sub(2, 4)                // only the nil tail
	is.Equal(len(tail.splitRecords), 0)

	// Source.Ack with [nil, nil] would overwrite the durable position.
	sourceMock.EXPECT().Ack(gomock.Any(), gomock.Any()).Times(0)

	worker, err := NewWorker(taskNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	err = worker.Ack(ctx, tail)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), CodeEmptySourcePosition.Reason())
}
