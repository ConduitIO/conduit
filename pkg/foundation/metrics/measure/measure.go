// Copyright © 2022 Meroxa, Inc.
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

package measure

import (
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/prometheus"
)

// Any changes in metrics defined below should also be reflected in the metrics documentation.
var (
	ConduitInfo = metrics.NewLabeledGauge("conduit_info",
		"Information about Conduit.",
		[]string{"version"})

	PipelinesGauge = metrics.NewLabeledGauge("conduit_pipelines",
		"Number of pipelines by status.",
		[]string{"status"})
	PipelineStatusGauge = metrics.NewLabeledGauge(
		"conduit_pipeline_status",
		"Pipeline statuses.",
		[]string{"pipeline_name", "status"},
	)
	PipelineRecoveringCount = metrics.NewLabeledCounter(
		"pipeline_recovering_count",
		"Number of times pipelines have been recovering (by by pipeline name)",
		[]string{"pipeline_name"},
	)
	ConnectorsGauge = metrics.NewLabeledGauge("conduit_connectors",
		"Number of connectors by type.",
		[]string{"type"})
	ProcessorsGauge = metrics.NewLabeledGauge("conduit_processors",
		"Number of processors by type.",
		[]string{"type"})
	InspectorsGauge = metrics.NewLabeledGauge(
		"conduit_inspector_sessions",
		"Number of inspector sessions by ID of pipeline component (connector or processor)",
		[]string{"component_id"},
	)

	ConnectorBytesHistogram = metrics.NewLabeledHistogram("conduit_connector_bytes",
		"Number of bytes a connector processed by pipeline name, plugin and type (source, destination).",
		[]string{"pipeline_name", "plugin", "type"},
		// buckets from 1KiB to 2MiB
		prometheus.HistogramOpts{Buckets: []float64{1024, 1024 << 1, 1024 << 2, 1024 << 3, 1024 << 4, 1024 << 5, 1024 << 6, 1024 << 7, 1024 << 8, 1024 << 9, 1024 << 10, 1024 << 11}},
	)
	DLQBytesHistogram = metrics.NewLabeledHistogram("conduit_dlq_bytes",
		"Number of bytes a DLQ connector processed per pipeline and plugin.",
		[]string{"pipeline_name", "plugin"},
		// buckets from 1KiB to 2MiB
		prometheus.HistogramOpts{Buckets: []float64{1024, 1024 << 1, 1024 << 2, 1024 << 3, 1024 << 4, 1024 << 5, 1024 << 6, 1024 << 7, 1024 << 8, 1024 << 9, 1024 << 10, 1024 << 11}},
	)
	PipelineExecutionDurationTimer = metrics.NewLabeledTimer("conduit_pipeline_execution_duration_seconds",
		"Amount of time records spent in a pipeline.",
		[]string{"pipeline_name"},
		prometheus.HistogramOpts{Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}},
	)
	ConnectorExecutionDurationTimer = metrics.NewLabeledTimer("conduit_connector_execution_duration_seconds",
		"Amount of time spent reading or writing records per pipeline, plugin and connector type (source, destination).",
		[]string{"pipeline_name", "plugin", "type"},
		prometheus.HistogramOpts{Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}},
	)
	ProcessorExecutionDurationTimer = metrics.NewLabeledTimer("conduit_processor_execution_duration_seconds",
		"Amount of time spent on processing records per pipeline and processor.",
		[]string{"pipeline_name", "processor"},
		prometheus.HistogramOpts{Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}},
	)
	DLQExecutionDurationTimer = metrics.NewLabeledTimer("conduit_dlq_execution_duration_seconds",
		"Amount of time spent writing records to DLQ connector per pipeline and plugin.",
		[]string{"pipeline_name", "plugin"},
		prometheus.HistogramOpts{Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}},
	)
)
