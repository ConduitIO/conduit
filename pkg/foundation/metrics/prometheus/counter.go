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

package prometheus

import (
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

type labeledCounter struct {
	pc *prometheus.CounterVec
}

func (lc *labeledCounter) WithValues(vs ...string) metrics.Counter {
	return &counter{pc: lc.pc.WithLabelValues(vs...)}
}

func (lc *labeledCounter) Describe(ch chan<- *prometheus.Desc) {
	lc.pc.Describe(ch)
}

func (lc *labeledCounter) Collect(ch chan<- prometheus.Metric) {
	lc.pc.Collect(ch)
}

type counter struct {
	pc prometheus.Counter
}

func (c *counter) Inc(vs ...float64) {
	if len(vs) == 0 {
		c.pc.Inc()
		return
	}

	c.pc.Add(sumFloat64(vs...))
}

func (c *counter) Describe(ch chan<- *prometheus.Desc) {
	c.pc.Describe(ch)
}

func (c *counter) Collect(ch chan<- prometheus.Metric) {
	c.pc.Collect(ch)
}
