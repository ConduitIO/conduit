// Copyright © 2024 Meroxa, Inc.
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

//go:generate mockgen -typed -destination=mock/destination.go -package=mock -mock_names=Destination=Destination . Destination

package funnel

import (
	"bytes"
	"context"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type DestinationTask struct {
	id          string
	destination Destination
	logger      log.CtxLogger

	metrics ConnectorMetrics
}

type Destination interface {
	ID() string
	Open(context.Context) error
	Write(context.Context, []opencdc.Record) error
	Ack(context.Context) ([]connector.DestinationAck, error)
	Teardown(context.Context) error
	// TODO figure out if we want to handle these errors. This returns errors
	//  coming from the persister, which persists the connector asynchronously.
	//  Are we even interested in these errors in the pipeline? Sounds like
	//  something we could surface and handle globally in the runtime instead.
	Errors() <-chan error
}

func NewDestinationTask(
	id string,
	destination Destination,
	logger log.CtxLogger,
	metrics ConnectorMetrics,
) *DestinationTask {
	logger = logger.WithComponent("task:destination")
	logger.Logger = logger.With().Str(log.ConnectorIDField, id).Logger()
	return &DestinationTask{
		id:          id,
		destination: destination,
		logger:      logger,
		metrics:     metrics,
	}
}

func (t *DestinationTask) ID() string {
	return t.id
}

func (t *DestinationTask) Open(ctx context.Context) error {
	t.logger.Debug(ctx).Msg("opening destination")
	err := t.destination.Open(ctx)
	if err != nil {
		return cerrors.Errorf("failed to open destination connector: %w", err)
	}
	t.logger.Debug(ctx).Msg("destination open")
	return nil
}

func (t *DestinationTask) Close(ctx context.Context) error {
	return t.destination.Teardown(ctx)
}

func (t *DestinationTask) Do(ctx context.Context, batch *Batch) error {
	records := batch.ActiveRecords()

	// Store the positions of the records in the batch to be used for
	// validation of acks.
	positions := make([]opencdc.Position, len(records))
	for i, rec := range records {
		positions[i] = rec.Position
	}

	start := time.Now()
	err := t.destination.Write(ctx, records)
	if err != nil {
		return cerrors.Errorf("failed to write %d records to destination: %w", len(positions), err)
	}

	activeIndices := batch.ActiveRecordIndices()
	var ackCount = 0
	for range len(positions) {
		acks, err := t.destination.Ack(ctx)
		if err != nil {
			return cerrors.Errorf("failed to receive acks for %d records from destination: %w", len(positions), err)
		}
		t.metrics.Observe(records[ackCount:ackCount+len(acks)], start)

		if err := t.validateAcks(acks, positions[ackCount:]); err != nil {
			return cerrors.Errorf("failed to validate acks: %w", err)
		}
		t.markBatchRecords(batch, activeIndices, ackCount, acks)

		ackCount += len(acks)
		if ackCount >= len(positions) {
			break
		}
	}

	return nil
}

func (t *DestinationTask) validateAcks(acks []connector.DestinationAck, positions []opencdc.Position) error {
	if len(acks) > len(positions) {
		return cerrors.Errorf("received %d acks, but expected at most %d", len(acks), len(positions))
	}

	for i, ack := range acks {
		if !bytes.Equal(positions[i], ack.Position) {
			return cerrors.Errorf("received unexpected ack, expected position %q but got %q", positions[i], ack.Position)
		}
	}

	return nil
}

func (t *DestinationTask) markBatchRecords(b *Batch, activeIndices []int, from int, acks []connector.DestinationAck) {
	for i, ack := range acks {
		if ack.Error != nil {
			idx := from + i
			if activeIndices != nil {
				idx = activeIndices[idx]
			}
			b.Nack(from+i, ack.Error)
		}
	}
}
