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

package prometheus_test

import (
	"os"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/prometheus"
	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

var (
	testCounter = metrics.NewCounter("prom_example_counter", "example")
	testTimer   = metrics.NewTimer("prom_example_timer", "example")
	//nolint:unused // the whole point of this is to show that even unused gauges show up in prometheus
	testGauge = metrics.NewGauge("prom_example_gauge", "example")
)

func ExampleNewRegistry() {
	// create a new registry and register it in the metrics package and as a
	// prometheus collector
	reg := prometheus.NewRegistry(map[string]string{"static_label": "example"})
	metrics.Register(reg)
	promclient.MustRegister(reg)

	// add a metric dynamically
	labeledCounter := metrics.NewLabeledCounter("prom_example_dynamic_labeled", "example", []string{"test_label"})

	// observe some metrics
	testCounter.Inc()
	testTimer.Update(time.Second)
	labeledCounter.WithValues("val1").Inc(100)
	labeledCounter.WithValues("val2")

	// gather and print metrics
	gatheredMetrics, err := promclient.DefaultGatherer.Gather()
	if err != nil {
		panic(err)
	}

	enc := expfmt.NewEncoder(os.Stdout, expfmt.FmtText)
	for _, m := range gatheredMetrics {
		if strings.HasPrefix(m.GetName(), "prom_example_") {
			err := enc.Encode(m)
			if err != nil {
				panic(err)
			}
		}
	}

	// Output:
	// # HELP prom_example_counter example
	// # TYPE prom_example_counter counter
	// prom_example_counter{static_label="example"} 1
	// # HELP prom_example_dynamic_labeled example
	// # TYPE prom_example_dynamic_labeled counter
	// prom_example_dynamic_labeled{static_label="example",test_label="val1"} 100
	// prom_example_dynamic_labeled{static_label="example",test_label="val2"} 0
	// # HELP prom_example_gauge example
	// # TYPE prom_example_gauge gauge
	// prom_example_gauge{static_label="example"} 0
	// # HELP prom_example_timer example
	// # TYPE prom_example_timer histogram
	// prom_example_timer_bucket{static_label="example",le="0.005"} 0
	// prom_example_timer_bucket{static_label="example",le="0.01"} 0
	// prom_example_timer_bucket{static_label="example",le="0.025"} 0
	// prom_example_timer_bucket{static_label="example",le="0.05"} 0
	// prom_example_timer_bucket{static_label="example",le="0.1"} 0
	// prom_example_timer_bucket{static_label="example",le="0.25"} 0
	// prom_example_timer_bucket{static_label="example",le="0.5"} 0
	// prom_example_timer_bucket{static_label="example",le="1"} 1
	// prom_example_timer_bucket{static_label="example",le="2.5"} 1
	// prom_example_timer_bucket{static_label="example",le="5"} 1
	// prom_example_timer_bucket{static_label="example",le="10"} 1
	// prom_example_timer_bucket{static_label="example",le="+Inf"} 1
	// prom_example_timer_sum{static_label="example"} 1
	// prom_example_timer_count{static_label="example"} 1
}
