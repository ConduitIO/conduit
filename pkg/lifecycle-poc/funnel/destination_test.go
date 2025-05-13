// Copyright © 2025 Meroxa, Inc.
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

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestDestinationTask_Do_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	destinationMock := mock.NewDestination(ctrl)

	records := randomRecords(10)
	batch := NewBatch(records)
	destinationMock.EXPECT().Write(ctx, records).Return(nil)

	acks := make([]connector.DestinationAck, len(records))
	for i, record := range records {
		acks[i] = connector.DestinationAck{
			Position: record.Position,
			Error:    nil,
		}
	}

	destinationMock.EXPECT().Ack(ctx).Return(acks, nil)

	task := NewDestinationTask("test", destinationMock, logger, NoOpConnectorMetrics{})
	err := task.Do(ctx, batch)
	is.NoErr(err)

	is.Equal(batch.ActiveRecords(), records)
	for _, status := range batch.recordStatuses {
		is.Equal(status, RecordStatus{Flag: RecordFlagAck})
	}
}
