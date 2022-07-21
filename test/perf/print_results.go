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

package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/docker/go-units"
	promclient "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

const pipelineName = "perf-test"

type stats struct {
	count uint64
	bytes float64
	time  time.Time
}

type metricsPrinter struct {
	firstStats stats
}

func (mp *metricsPrinter) print() {
	metricFamilies, err := mp.getMetrics()
	if err != nil {
		fmt.Printf("failed getting metrics: %v", err)
		os.Exit(1)
	}

	count, totalTime, err := mp.getPipelineMetrics(metricFamilies)
	if err != nil {
		fmt.Printf("failed getting pipeline metrics: %v", err)
		os.Exit(1)
	}
	recordsPerSec := float64(count) / totalTime
	totalSize := mp.getSourceByteMetrics(metricFamilies)
	bytesPerSec := units.HumanSize(totalSize / totalTime)
	pipelineRate := (count - mp.firstStats.count) / uint64(time.Since(mp.firstStats.time).Seconds())

	in := `
| total records | rec/s (Conduit) | rec/s (pipeline) | bytes/s | time |
|---------------|-----------------|------------------|---------|------|
| %v            | %v              | %v               | %v      | %v   |


`
	in = fmt.Sprintf(in, count, recordsPerSec, pipelineRate, bytesPerSec, time.Now().Format(time.RFC3339))
	r, _ := glamour.NewTermRenderer(
		// detect background color and pick either the default dark or light theme
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(200),
	)

	out, err := r.Render(in)
	fmt.Print(out)

	if mp.firstStats == (stats{}) {
		mp.firstStats = stats{
			count: count,
			bytes: totalSize,
			time:  time.Now(),
		}
	}
}

// getMetrics returns all the metrics which Conduit exposes
func (mp *metricsPrinter) getMetrics() (map[string]*promclient.MetricFamily, error) {
	metrics, err := http.Get("http://localhost:8080/metrics")
	if err != nil {
		fmt.Printf("failed getting metrics: %v", err)
		os.Exit(1)
	}
	defer metrics.Body.Close()

	var parser expfmt.TextParser
	return parser.TextToMetricFamilies(metrics.Body)
}

// getPipelineMetrics extract the test pipeline's metrics
// (total number of records, time records spent in pipeline)
func (mp *metricsPrinter) getPipelineMetrics(families map[string]*promclient.MetricFamily) (uint64, float64, error) {
	family, ok := families["conduit_pipeline_execution_duration_seconds"]
	if !ok {
		return 0, 0, errors.New("metric family conduit_pipeline_execution_duration_seconds not available")
	}

	for _, m := range family.Metric {
		if hasLabel(m, "pipeline_name", pipelineName) {
			return *m.Histogram.SampleCount, *m.Histogram.SampleSum, nil
		}
	}

	return 0, 0, fmt.Errorf("metrics for pipeline %q not found", pipelineName)
}

// getSourceByteMetrics returns the amount of bytes the sources in the test pipeline produced
func (mp *metricsPrinter) getSourceByteMetrics(families map[string]*promclient.MetricFamily) float64 {
	for _, m := range families["conduit_connector_bytes"].Metric {
		if hasLabel(m, "pipeline_name", pipelineName) && hasLabel(m, "type", "source") {
			return *m.Histogram.SampleSum
		}
	}

	return 0
}

// hasLabel returns true, if the input metrics has a label with the given name and value
func hasLabel(m *promclient.Metric, name string, value string) bool {
	for _, labelPair := range m.GetLabel() {
		if labelPair.GetName() == name && labelPair.GetValue() == value {
			return true
		}
	}
	return false
}

func main() {
	interval := flag.Duration(
		"interval",
		10*time.Second,
		"interval at which the current performance results will be printed.",
	)
	duration := flag.Duration(
		"duration",
		10*time.Minute,
		"duration of the performance test",
	)

	fmt.Println(
		"When interpreting test results, please take into account, " +
			"that if built-in plugins are used, their resource usage is part of Conduit's usage too.",
	)
	until := time.Now().Add(*duration)
	mp := metricsPrinter{}
	for {
		mp.print()
		if time.Now().After(until) {
			break
		}
		time.Sleep(*interval)
	}
}
