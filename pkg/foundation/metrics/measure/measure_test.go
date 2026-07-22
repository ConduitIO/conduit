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

package measure_test

import (
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
	condprom "github.com/conduitio/conduit/pkg/foundation/metrics/prometheus"
	"github.com/matryer/is"
	promclient "github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// registerFreshBackend registers a fresh conduit metrics.Registry against the
// global measure.go specs (mirroring what pkg/conduit/runtime.go does at
// startup) and returns a throwaway prometheus.Registry that scrapes only that
// backend. It must be called BEFORE any WithValues(...) call under test:
// WithValues fans out over the registries attached at call time, so a
// registry registered afterwards would not observe those values. Every test
// gets its own backing registry, so tests don't interfere with each other
// even though measure.go's metric variables are package-level globals shared
// across the test binary.
func registerFreshBackend(is *is.I) *promclient.Registry {
	reg := condprom.NewRegistry(nil)
	metrics.Register(reg)

	promReg := promclient.NewRegistry()
	is.NoErr(promReg.Register(reg))
	return promReg
}

// findMetric returns the *dto.Metric in family with the given labels
// (name -> value), or nil if no metric matches.
func findMetric(family *dto.MetricFamily, labels map[string]string) *dto.Metric {
	for _, m := range family.GetMetric() {
		got := make(map[string]string, len(m.GetLabel()))
		for _, l := range m.GetLabel() {
			got[l.GetName()] = l.GetValue()
		}
		match := len(got) == len(labels)
		if match {
			for k, v := range labels {
				if got[k] != v {
					match = false
					break
				}
			}
		}
		if match {
			return m
		}
	}
	return nil
}

func findFamily(families []*dto.MetricFamily, name string) *dto.MetricFamily {
	for _, f := range families {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

// TestConnectorBytesHistogram_ComponentIDLabel asserts that
// conduit_connector_bytes (P7) carries a component_id label set to the
// connector instance ID, alongside all pre-existing labels (additive change,
// nothing removed).
func TestConnectorBytesHistogram_ComponentIDLabel(t *testing.T) {
	is := is.New(t)

	const (
		pipelineName = "test-pipeline-connector-bytes"
		plugin       = "test-plugin"
		connType     = "source"
		componentID  = "test-connector-instance-id"
	)

	promReg := registerFreshBackend(is)

	h := measure.ConnectorBytesHistogram.WithValues(pipelineName, plugin, connType, componentID)
	h.Observe(1024)

	families, err := promReg.Gather()
	is.NoErr(err)
	family := findFamily(families, "conduit_connector_bytes")
	is.True(family != nil) // conduit_connector_bytes family must be present

	m := findMetric(family, map[string]string{
		"pipeline_name": pipelineName,
		"plugin":        plugin,
		"type":          connType,
		"component_id":  componentID,
	})
	is.True(m != nil) // expected a conduit_connector_bytes series with all 4 labels, including component_id
	is.Equal(m.GetHistogram().GetSampleCount(), uint64(1))
}

// TestConnectorExecutionDurationTimer_ComponentIDLabel asserts that
// conduit_connector_execution_duration_seconds (P7) carries a component_id
// label set to the connector instance ID, alongside all pre-existing labels.
func TestConnectorExecutionDurationTimer_ComponentIDLabel(t *testing.T) {
	is := is.New(t)

	const (
		pipelineName = "test-pipeline-connector-duration"
		plugin       = "test-plugin"
		connType     = "destination"
		componentID  = "test-connector-instance-id-2"
	)

	promReg := registerFreshBackend(is)

	timer := measure.ConnectorExecutionDurationTimer.WithValues(pipelineName, plugin, connType, componentID)
	timer.Update(10 * time.Millisecond)

	families, err := promReg.Gather()
	is.NoErr(err)
	family := findFamily(families, "conduit_connector_execution_duration_seconds")
	is.True(family != nil) // conduit_connector_execution_duration_seconds family must be present

	m := findMetric(family, map[string]string{
		"pipeline_name": pipelineName,
		"plugin":        plugin,
		"type":          connType,
		"component_id":  componentID,
	})
	is.True(m != nil) // expected a conduit_connector_execution_duration_seconds series with all 4 labels, including component_id
	is.Equal(m.GetHistogram().GetSampleCount(), uint64(1))
}

// TestProcessorExecutionDurationTimer_ComponentIDLabel asserts that
// conduit_processor_execution_duration_seconds (P7) carries a component_id
// label set to the processor instance ID, alongside all pre-existing labels.
func TestProcessorExecutionDurationTimer_ComponentIDLabel(t *testing.T) {
	is := is.New(t)

	const (
		pipelineName = "test-pipeline-processor-duration"
		plugin       = "test-processor-plugin"
		componentID  = "test-processor-instance-id"
	)

	promReg := registerFreshBackend(is)

	timer := measure.ProcessorExecutionDurationTimer.WithValues(pipelineName, plugin, componentID)
	timer.Update(5 * time.Millisecond)

	families, err := promReg.Gather()
	is.NoErr(err)
	family := findFamily(families, "conduit_processor_execution_duration_seconds")
	is.True(family != nil) // conduit_processor_execution_duration_seconds family must be present

	m := findMetric(family, map[string]string{
		"pipeline_name": pipelineName,
		"processor":     plugin,
		"component_id":  componentID,
	})
	is.True(m != nil) // expected a conduit_processor_execution_duration_seconds series with all 3 labels, including component_id
	is.Equal(m.GetHistogram().GetSampleCount(), uint64(1))
}
