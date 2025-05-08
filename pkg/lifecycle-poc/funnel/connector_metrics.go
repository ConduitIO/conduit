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
	"strings"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
)

type ConnectorMetrics interface {
	Observe(records []opencdc.Record, start time.Time)
}

type NoOpConnectorMetrics struct{}

func (m NoOpConnectorMetrics) Observe([]opencdc.Record, time.Time) {}

type ConnectorMetricsImpl struct {
	timer     metrics.Timer
	histogram metrics.RecordBytesHistogram
}

func NewConnectorMetrics(pipelineName, pluginName string, connType connector.Type) ConnectorMetricsImpl {
	timer := measure.ConnectorExecutionDurationTimer.WithValues(
		pipelineName,
		pluginName,
		strings.ToLower(connType.String()),
	)

	histogram := measure.ConnectorBytesHistogram.WithValues(
		pipelineName,
		pluginName,
		strings.ToLower(connType.String()),
	)

	return ConnectorMetricsImpl{
		timer:     timer,
		histogram: metrics.NewRecordBytesHistogram(histogram),
	}
}

func (m ConnectorMetricsImpl) Observe(records []opencdc.Record, start time.Time) {
	// Precalculate sizes so that we don't need to hold a reference to records
	// and observations can happen in a goroutine.
	sizes := make([]float64, len(records))
	for i, rec := range records {
		sizes[i] = m.histogram.SizeOf(rec)
	}
	tookPerRecord := time.Since(start) / time.Duration(len(sizes))
	go func() {
		for i := range len(sizes) {
			m.timer.Update(tookPerRecord)
			m.histogram.H.Observe(sizes[i])
		}
	}()
}
