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

//go:generate mockgen -typed -destination=mock/source.go -package=mock -mock_names=Source=Source . Source

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
	Teardown(context.Context) error
	// TODO figure out if we want to handle these errors. This returns errors
	//  coming from the persister, which persists the connector asynchronously.
	//  Are we even interested in these errors in the pipeline? Sounds like
	//  something we could surface and handle globally in the runtime instead.
	Errors() <-chan error
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

func (t *SourceTask) Close(context.Context) error {
	// source is torn down in the worker on stop
	return nil
}

func (t *SourceTask) Do(ctx context.Context, b *Batch) error {
	start := time.Now()

	recs, err := t.source.Read(ctx)
	if err != nil {
		return cerrors.Errorf("failed to read from source: %w", err)
	}

	t.observeMetrics(recs, start)

	// Overwrite the batch with the new records.
	*b = *NewBatch(recs)
	return nil
}

func (t *SourceTask) observeMetrics(records []opencdc.Record, start time.Time) {
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

func (t *SourceTask) GetSource() Source {
	return t.source
}
