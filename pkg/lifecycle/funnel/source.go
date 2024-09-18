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
	"context"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
)

type SourceTask struct {
	id     string
	source Source
	logger log.CtxLogger

	timer     metrics.Timer
	histogram metrics.RecordBytesHistogram
}

type Source interface {
	ID() string
	Open(context.Context) error
	Read(context.Context) ([]opencdc.Record, error)
	Ack(context.Context, []opencdc.Position) error
	Stop(context.Context) (opencdc.Position, error)
	Teardown(context.Context) error
	Errors() <-chan error // TODO use
}

func NewSourceTask(
	id string,
	source Source,
	logger log.CtxLogger,
	timer metrics.Timer,
	histogram metrics.Histogram,
) *SourceTask {
	logger = logger.WithComponent("task:source")
	logger.Logger = logger.With().Str(log.ConnectorIDField, id).Logger()
	return &SourceTask{
		id:        id,
		source:    source,
		logger:    logger,
		timer:     timer,
		histogram: metrics.NewRecordBytesHistogram(histogram),
	}
}

func (t *SourceTask) ID() string {
	return t.id
}

func (t *SourceTask) Open(ctx context.Context) error {
	t.logger.Debug(ctx).Msg("opening source")
	err := t.source.Open(ctx)
	if err != nil {
		return cerrors.Errorf("failed to open source connector: %w", err)
	}
	t.logger.Debug(ctx).Msg("source open")
	return nil
}

func (t *SourceTask) Do(ctx context.Context, _ []opencdc.Record, next Tasks) ([]opencdc.Record, error) {
	recs, err := t.source.Read(ctx)
	if err != nil {
		return nil, cerrors.Errorf("failed to read from source: %w", err)
	}

	positions := make([]opencdc.Position, len(recs))
	for i, rec := range recs {
		positions[i] = rec.Position
	}

	start := time.Now()
	out, err := next.Do(ctx, recs)
	if err != nil {
		return nil, cerrors.Errorf("failed to process records: %w", err)
	}

	// Acknowledge the records.
	err = t.source.Ack(ctx, positions)
	if err != nil {
		return nil, cerrors.Errorf("failed to ack records: %w", err)
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

	return out, nil
}

func (t *SourceTask) Close(ctx context.Context) error {
	var errs []error

	_, err := t.source.Stop(ctx)
	errs = append(errs, err)
	err = t.source.Teardown(ctx)
	errs = append(errs, err)

	return cerrors.Join(errs...)
}
