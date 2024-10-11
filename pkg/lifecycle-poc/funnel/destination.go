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
	"github.com/conduitio/conduit/pkg/foundation/metrics"
)

type DestinationTask struct {
	id          string
	destination Destination
	logger      log.CtxLogger

	timer     metrics.Timer
	histogram metrics.RecordBytesHistogram
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
	timer metrics.Timer,
	histogram metrics.Histogram,
) *DestinationTask {
	logger = logger.WithComponent("task:destination")
	logger.Logger = logger.With().Str(log.ConnectorIDField, id).Logger()
	return &DestinationTask{
		id:          id,
		destination: destination,
		logger:      logger,
		timer:       timer,
		histogram:   metrics.NewRecordBytesHistogram(histogram),
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
	positions := make([]opencdc.Position, len(records))
	for i, rec := range records {
		positions[i] = rec.Position
	}

	start := time.Now()
	err := t.destination.Write(ctx, records)
	if err != nil {
		return cerrors.Errorf("failed to write %d records to destination: %w", len(positions), err)
	}

	acks := make([]connector.DestinationAck, 0, len(positions))
	for len(acks) != len(positions) {
		acksResp, err := t.destination.Ack(ctx)
		if err != nil {
			return cerrors.Errorf("failed to receive acks for %d records from destination: %w", len(positions), err)
		}
		t.observeMetrics(records[len(acks):len(acks)+len(acksResp)], start)
		acks = append(acks, acksResp...)
	}

	for i, ack := range acks {
		if !bytes.Equal(positions[i], ack.Position) {
			// TODO is this a fatal error? Looks like a bug in the connector
			return cerrors.Errorf("received unexpected ack, expected position %q but got %q", positions[i], ack.Position)
		}
		if ack.Error != nil {
			batch.Nack(i, ack.Error)
		}
	}

	return nil
}

func (t *DestinationTask) observeMetrics(records []opencdc.Record, start time.Time) {
	// Precalculate sizes so that we don't need to hold a reference to records
	// and observations can happen in a goroutine.
	sizes := make([]float64, len(records))
	for i, rec := range records {
		sizes[i] = t.histogram.SizeOf(rec)
	}
	tookPerRecord := time.Since(start) / time.Duration(len(sizes))
	go func() {
		for i := range len(sizes) {
			t.timer.Update(tookPerRecord)
			t.histogram.H.Observe(sizes[i])
		}
	}()
}
