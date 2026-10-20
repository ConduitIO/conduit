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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// TestSourceTask_Do_ReadFailure_ObservesError asserts that a failed source read
// is recorded on the connector error counter before the error propagates. This
// is the source half of the connector observability slice (#3013): record and
// error counts per connector, not just bytes and latency.
func TestSourceTask_Do_ReadFailure_ObservesError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Test(t)

	ctrl := gomock.NewController(t)
	sourceMock := NewMockSource(ctrl)

	wantErr := cerrors.New("error")
	sourceMock.EXPECT().Read(ctx).Return(nil, wantErr)

	recs := &recordingCounter{}
	errs := &recordingCounter{}
	connMetrics := ConnectorMetricsImpl{
		timer:     noop.Timer{},
		histogram: metrics.NewRecordBytesHistogram(noop.Histogram{}),
		records:   recs,
		errors:    errs,
	}

	task := NewSourceTask("test", sourceMock, logger, connMetrics)
	err := task.Do(ctx, &Batch{})
	is.True(cerrors.Is(err, wantErr))

	// The read failed before any record was observed, so exactly one error and
	// no records.
	is.Equal(errs.total, float64(1))
	is.Equal(recs.total, float64(0))
}
