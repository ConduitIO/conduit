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

type labeledGauge struct {
	pg *prometheus.GaugeVec
}

func (lg *labeledGauge) WithValues(labels ...string) metrics.Gauge {
	return &gauge{pg: lg.pg.WithLabelValues(labels...)}
}

func (lg *labeledGauge) DeleteLabels(labels ...string) bool {
	return lg.pg.DeleteLabelValues(labels...)
}

func (lg *labeledGauge) Describe(c chan<- *prometheus.Desc) {
	lg.pg.Describe(c)
}

func (lg *labeledGauge) Collect(c chan<- prometheus.Metric) {
	lg.pg.Collect(c)
}

type gauge struct {
	pg prometheus.Gauge
}

func (g *gauge) Inc(vs ...float64) {
	if len(vs) == 0 {
		g.pg.Inc()
		return
	}
	g.pg.Add(sumFloat64(vs...))
}

func (g *gauge) Dec(vs ...float64) {
	if len(vs) == 0 {
		g.pg.Dec()
		return
	}

	g.pg.Add(-sumFloat64(vs...))
}

func (g *gauge) Set(v float64) {
	g.pg.Set(v)
}

func (g *gauge) Describe(c chan<- *prometheus.Desc) {
	g.pg.Describe(c)
}

func (g *gauge) Collect(c chan<- prometheus.Metric) {
	g.pg.Collect(c)
}
