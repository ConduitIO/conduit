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
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
)

// TestConnectorMetricsImpl_Observe_EmptyBatch_NoPanic is the regression test
// for a reachable divide-by-zero PANIC in the metrics path — the path
// v0.20 makes the DEFAULT configuration.
//
// Observe computed `time.Since(start) / time.Duration(len(sizes))` before
// spawning its goroutine, so an empty batch panicked on the WORKER's own
// goroutine. A panic on the data path is unrecoverable: it kills the whole
// Conduit process and every pipeline in it, not just the offending one — the
// same severity class as #2726.
//
// Both call sites can pass an empty slice: SourceTask.Do observes whatever
// source.Read returned (connector.Source.Read does not reject an empty Records
// slice, and a non-Go plugin is not bound by the Go SDK's habit of skipping
// empties), and DestinationTask.Do observes records[ackCount:ackCount+len(acks)]
// where validateAcks explicitly permits len(acks) == 0.
//
// Note the contrast that made this easy to miss: ProcessorTask.Do already
// rejects an empty result. The connector path did not.
func TestConnectorMetricsImpl_Observe_EmptyBatch_NoPanic(t *testing.T) {
	m := ConnectorMetricsImpl{
		timer:     noop.Timer{},
		histogram: metrics.NewRecordBytesHistogram(noop.Histogram{}),
	}

	// Would panic with "integer divide by zero" before the guard.
	m.Observe(nil, time.Now())
	m.Observe([]opencdc.Record{}, time.Now())

	// A non-empty batch must still be observed normally.
	m.Observe([]opencdc.Record{{Position: opencdc.Position("p0")}}, time.Now())
}
