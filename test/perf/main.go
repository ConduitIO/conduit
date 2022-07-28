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
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"github.com/charmbracelet/glamour"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/docker/go-units"
	promclient "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

const pipelineName = "perf-test"

type metrics struct {
	count         uint64
	bytes         float64
	measuredAt    time.Time
	recordsPerSec float64
	msPerRec      float64
	pipelineRate  uint64
	bytesPerSec   string
}

func (m metrics) msPerRecStr() string {
	return strconv.FormatFloat(m.msPerRec, 'f', 10, 64)
}

var printerTypes = []string{"csv", "console"}

type printer interface {
	init() error
	print(metrics) error
}

func newPrinter(printerType string, workload string) (printer, error) {
	var p printer
	switch printerType {
	case "console":
		p = &consolePrinter{workload: workload}
	case "csv":
		p = &csvPrinter{workload: workload}
	default:
		return nil, fmt.Errorf("unknown printer type %q, possible values: %v", printerType, printerTypes)
	}
	err := p.init()
	if err != nil {
		return nil, fmt.Errorf("failed initializing printer: %w", err)
	}
	return p, nil
}

type csvPrinter struct {
	writer   *csv.Writer
	workload string
}

func (c *csvPrinter) init() error {
	file, err := os.Create(
		fmt.Sprintf("./performance-test-results-%v.csv", time.Now().Format("2006-01-02-15-04-05")),
	)
	if err != nil {
		return fmt.Errorf("failed creating file: %w", err)
	}
	c.writer = csv.NewWriter(file)
	err = c.writer.Write([]string{
		"workload",
		"total records",
		"rec/s (Conduit)",
		"ms/record (Conduit)",
		"rec/s (pipeline)",
		"bytes/s",
		"measured_at",
	})
	if err != nil {
		return err
	}
	c.writer.Flush()
	return nil
}

func (c *csvPrinter) print(m metrics) error {
	err := c.writer.Write([]string{
		c.workload,
		fmt.Sprintf("%v", m.count),
		fmt.Sprintf("%v", m.recordsPerSec),
		m.msPerRecStr(),
		fmt.Sprintf("%v", m.pipelineRate),
		fmt.Sprintf("%v", m.bytesPerSec),
		fmt.Sprintf("%v", m.measuredAt.Format(time.RFC3339)),
	})
	if err != nil {
		return err
	}
	c.writer.Flush()
	return nil
}

type consolePrinter struct {
	renderer *glamour.TermRenderer
	workload string
}

func (c *consolePrinter) init() error {
	r, _ := glamour.NewTermRenderer(
		// detect background color and pick either the default dark or light theme
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(200),
	)
	c.renderer = r
	return nil
}

func (c *consolePrinter) print(m metrics) error {
	in := `
| workload | total records | rec/s (Conduit) | ms/record (Conduit) | rec/s (pipeline) | bytes/s | measured at |
|----------|---------------|-----------------|---------------------|------------------|---------|-------------|
| %v       | %v            | %v              | %v                  | %v               | %v      | %v          |


`
	in = fmt.Sprintf(
		in,
		c.workload,
		m.count,
		m.recordsPerSec,
		m.msPerRecStr(),
		m.pipelineRate,
		m.bytesPerSec,
		m.measuredAt.Format(time.RFC3339),
	)
	out, err := c.renderer.Render(in)
	if err != nil {
		return fmt.Errorf("failed rendering output: %w", err)
	}
	fmt.Print(out)
	return nil
}

type collector struct {
	first metrics
}

func newCollector() (collector, error) {
	c := collector{}
	err := c.init()
	if err != nil {
		return collector{}, fmt.Errorf("failed initializing collector: %w", err)
	}
	return c, err
}

func (c *collector) init() error {
	first, err := c.collect()
	if err != nil {
		return err
	}
	c.first = first
	return nil
}

func (c *collector) collect() (metrics, error) {
	metricFamilies, err := c.getMetrics()
	if err != nil {
		return metrics{}, fmt.Errorf("failed getting metrics: %v", err)
	}

	m := metrics{}
	count, totalTime, err := c.getPipelineMetrics(metricFamilies)
	if err != nil {
		fmt.Printf("failed getting pipeline metrics: %v", err)
		os.Exit(1)
	}
	m.count = count
	m.recordsPerSec = float64(count) / totalTime
	m.msPerRec = (totalTime / float64(count)) / 1000
	m.bytes = c.getSourceByteMetrics(metricFamilies)
	m.bytesPerSec = units.HumanSize(m.bytes / totalTime)
	m.pipelineRate = (count - c.first.count) / uint64(time.Since(c.first.measuredAt).Seconds())
	m.measuredAt = time.Now()

	return m, nil
}

// getMetrics returns all the metrics which Conduit exposes
func (c *collector) getMetrics() (map[string]*promclient.MetricFamily, error) {
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
func (c *collector) getPipelineMetrics(families map[string]*promclient.MetricFamily) (uint64, float64, error) {
	family, ok := families["conduit_pipeline_execution_duration_seconds"]
	if !ok {
		return 0, 0, errors.New("metric family conduit_pipeline_execution_duration_seconds not available")
	}

	for _, m := range family.Metric {
		if c.hasLabel(m, "pipeline_name", pipelineName) {
			return *m.Histogram.SampleCount, *m.Histogram.SampleSum, nil
		}
	}

	return 0, 0, fmt.Errorf("metrics for pipeline %q not found", pipelineName)
}

// getSourceByteMetrics returns the amount of bytes the sources in the test pipeline produced
func (c *collector) getSourceByteMetrics(families map[string]*promclient.MetricFamily) float64 {
	for _, m := range families["conduit_connector_bytes"].Metric {
		if c.hasLabel(m, "pipeline_name", pipelineName) && c.hasLabel(m, "type", "source") {
			return *m.Histogram.SampleSum
		}
	}

	return 0
}

// hasLabel returns true, if the input metrics has a label with the given name and value
func (c *collector) hasLabel(m *promclient.Metric, name string, value string) bool {
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
		"interval at which the current performance results will be collected and printed.",
	)
	duration := flag.Duration(
		"duration",
		10*time.Minute,
		"duration for which the metrics will be collected and printed",
	)
	printTo := flag.String(
		"print-to",
		"console",
		"where the metrics will be printed ('csv' to print to a CSV file, or 'console' to print to console",
	)
	workload := flag.String(
		"workload",
		"",
		"workload script",
	)
	flag.Parse()

	fmt.Println(
		"When interpreting test results, please take into account, " +
			"that if built-in plugins are used, their resource usage is part of Conduit's usage too.",
	)
	until := time.Now().Add(*duration)
	c, err := newCollector()
	if err != nil {
		fmt.Printf("couldn't create collector: %v", err)
		os.Exit(1)
	}

	p, err := newPrinter(*printTo, *workload)
	if err != nil {
		fmt.Printf("couldn't create printer: %v", err)
		os.Exit(1)
	}

	for {
		time.Sleep(*interval)
		metrics, err := c.collect()
		if err != nil {
			fmt.Printf("couldn't collect metrics: %v", err)
			os.Exit(1)
		}
		p.print(metrics)
		if time.Now().After(until) {
			break
		}
	}
}
