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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
)

type ProcessorMetrics interface {
	Observe(recordsNum int, start time.Time)
}

type NoOpProcessorMetrics struct{}

func (m NoOpProcessorMetrics) Observe(int, time.Time) {}

type ProcessorMetricsImpl struct {
	timer metrics.Timer
}

func (m ProcessorMetricsImpl) Observe(recordsNum int, start time.Time) {
	tookPerRecord := time.Since(start) / time.Duration(recordsNum)
	go func() {
		for range recordsNum {
			m.timer.Update(tookPerRecord)
		}
	}()
}

// NewProcessorMetrics builds the shared processor latency metric for one
// processor instance. componentID is the processor's instance ID (same ID
// space as the topology nodes and the conduit_inspector_* metrics), recorded
// via the component_id label so per-node dashboards can attribute latency to
// a specific processor instance.
func NewProcessorMetrics(pipelineName, plugin, componentID string) ProcessorMetricsImpl {
	return ProcessorMetricsImpl{
		timer: measure.ProcessorExecutionDurationTimer.WithValues(pipelineName, plugin, componentID),
	}
}
