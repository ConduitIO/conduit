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
	"strconv"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type SourceTask struct {
	id     string
	source Source
	logger log.CtxLogger
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
) *SourceTask {
	logger = logger.WithComponent("task:source")
	logger.Logger = logger.With().Str(log.ConnectorIDField, id).Logger()
	return &SourceTask{
		id:     id,
		source: source,
		logger: logger,
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

func (t *SourceTask) Close(ctx context.Context) error {
	var errs []error

	_, err := t.source.Stop(ctx)
	errs = append(errs, err)
	err = t.source.Teardown(ctx)
	errs = append(errs, err)

	return cerrors.Join(errs...)
}

func (t *SourceTask) Do(ctx context.Context, b *Batch) error {
	recs, err := t.source.Read(ctx)
	if err != nil {
		return cerrors.Errorf("failed to read from source: %w", err)
	}

	sourceID := t.source.ID()
	now := strconv.FormatInt(time.Now().UnixNano(), 10)
	for i, rec := range recs {
		if rec.Metadata == nil {
			rec.Metadata = opencdc.Metadata{
				opencdc.MetadataReadAt:                   now,
				opencdc.MetadataConduitSourceConnectorID: sourceID,
			}
		} else {
			if rec.Metadata[opencdc.MetadataReadAt] == "" {
				rec.Metadata[opencdc.MetadataReadAt] = now
			}
			if rec.Metadata[opencdc.MetadataConduitSourceConnectorID] == "" {
				rec.Metadata[opencdc.MetadataConduitSourceConnectorID] = sourceID
			}
		}
		recs[i] = rec
	}

	// Overwrite the batch with the new records.
	*b = *NewBatch(recs)
	return nil
}

func (t *SourceTask) GetSource() Source {
	return t.source
}
