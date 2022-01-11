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

type labeledHistogram struct {
	ph *prometheus.HistogramVec
}

func (lt *labeledHistogram) WithValues(labels ...string) metrics.Histogram {
	return &histogram{ph: lt.ph.WithLabelValues(labels...).(prometheus.Histogram)}
}

func (lt *labeledHistogram) Describe(c chan<- *prometheus.Desc) {
	lt.ph.Describe(c)
}

func (lt *labeledHistogram) Collect(c chan<- prometheus.Metric) {
	lt.ph.Collect(c)
}

type histogram struct {
	ph prometheus.Histogram
}

func (t *histogram) Observe(v float64) {
	t.ph.Observe(v)
}

func (t *histogram) Describe(c chan<- *prometheus.Desc) {
	c <- t.ph.Desc()
}

func (t *histogram) Collect(c chan<- prometheus.Metric) {
	t.ph.Collect(c)
}
