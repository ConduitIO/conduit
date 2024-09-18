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
	Stop(context.Context, opencdc.Position) error
	Teardown(context.Context) error
	Errors() <-chan error // TODO use
}

func NewDestinationTask(
	id string,
	destination Destination,
	logger log.CtxLogger,
	timer metrics.Timer,
	histogram metrics.Histogram,
) *DestinationTask {
	logger = logger.WithComponent("task:source")
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

func (t *DestinationTask) Do(ctx context.Context, recs []opencdc.Record, _ Tasks) ([]opencdc.Record, error) {
	positions := make([]opencdc.Position, len(recs))
	for i, rec := range recs {
		positions[i] = rec.Position
	}

	start := time.Now()
	err := t.destination.Write(ctx, recs)
	if err != nil {
		return nil, cerrors.Errorf("failed to write records to destination: %w", err)
	}

	acks, err := t.destination.Ack(ctx)
	if err != nil {
		return nil, cerrors.Errorf("failed to ack records: %w", err)
	}

	if len(acks) != len(positions) {
		return nil, cerrors.Errorf("expected %d acks, got %d", len(positions), len(acks))
	}
	var errs []error
	for i, ack := range acks {
		if !bytes.Equal(positions[i], ack.Position) {
			return nil, cerrors.Errorf("received unexpected ack, expected position %q but got %q", positions[i], ack.Position)
		}
		if ack.Error != nil {
			errs = append(errs, ack.Error)
		}
	}

	// Update metrics.
	for _, rec := range recs {
		readAt, err := rec.Metadata.GetReadAt()
		if err != nil {
			// If the plugin did not set the field fallback to the time Conduit
			// received the record (now).
			readAt = start
		}
		t.timer.UpdateSince(readAt)
		t.histogram.Observe(rec)
	}

	return recs, cerrors.Join(errs...)
}

func (t *DestinationTask) Close(ctx context.Context) error {
	var errs []error

	err := t.destination.Stop(ctx, nil)
	errs = append(errs, err)
	err = t.destination.Teardown(ctx)
	errs = append(errs, err)

	return cerrors.Join(errs...)
}
