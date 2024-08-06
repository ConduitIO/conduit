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
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// NewRegistry returns a registry that is responsible for managing a collection
// of metrics.
//
// Labels allows constant labels to be added to all metrics created in this
// registry, although this parameter should be used responsibly. See also
// https://prometheus.io/docs/instrumenting/writing_exporters/#target-labels,-not-static-scraped-labels
func NewRegistry(labels map[string]string) *Registry {
	return &Registry{
		labels: labels,
	}
}

// Registry describes a set of metrics. It implements metrics.Registry as well
// as prometheus.Collector and can thus be used as an adapter to collect Conduit
// metrics and deliver them to the prometheus client.
type Registry struct {
	labels  map[string]string
	mu      sync.Mutex
	metrics []prometheus.Collector
}

func (r *Registry) NewCounter(name, help string, opts ...metrics.Option) metrics.Counter {
	c := &counter{pc: prometheus.NewCounter(r.newCounterOpts(name, help, opts))}
	r.add(c)
	return c
}

func (r *Registry) NewLabeledCounter(name, help string, labels []string, opts ...metrics.Option) metrics.LabeledCounter {
	c := &labeledCounter{pc: prometheus.NewCounterVec(r.newCounterOpts(name, help, opts), labels)}
	r.add(c)
	return c
}

func (r *Registry) newCounterOpts(name, help string, opts []metrics.Option) prometheus.CounterOpts {
	return r.applyCounterOptions(
		prometheus.CounterOpts{
			Name:        name,
			Help:        help,
			ConstLabels: r.labels,
		},
		opts,
	)
}

func (r *Registry) applyCounterOptions(promOpts prometheus.CounterOpts, metricsOpts []metrics.Option) prometheus.CounterOpts {
	for _, mopt := range metricsOpts {
		opt, ok := mopt.(option)
		if !ok {
			// skip non-prometheus options
			continue
		}
		promOpts = opt.(counterOption).apply(promOpts)
	}
	return promOpts
}

func (r *Registry) NewGauge(name, help string, opts ...metrics.Option) metrics.Gauge {
	g := &gauge{
		pg: prometheus.NewGauge(r.newGaugeOpts(name, help, opts)),
	}
	r.add(g)
	return g
}

func (r *Registry) NewLabeledGauge(name, help string, labels []string, opts ...metrics.Option) metrics.LabeledGauge {
	g := &labeledGauge{
		pg: prometheus.NewGaugeVec(r.newGaugeOpts(name, help, opts), labels),
	}
	r.add(g)
	return g
}

func (r *Registry) newGaugeOpts(name, help string, opts []metrics.Option) prometheus.GaugeOpts {
	return r.applyGaugeOptions(
		prometheus.GaugeOpts{
			Name:        name,
			Help:        help,
			ConstLabels: r.labels,
		},
		opts,
	)
}

func (r *Registry) applyGaugeOptions(promOpts prometheus.GaugeOpts, metricsOpts []metrics.Option) prometheus.GaugeOpts {
	for _, mopt := range metricsOpts {
		opt, ok := mopt.(option)
		if !ok {
			// skip non-prometheus options
			continue
		}
		promOpts = opt.(gaugeOption).apply(promOpts)
	}
	return promOpts
}

func (r *Registry) NewTimer(name, help string, opts ...metrics.Option) metrics.Timer {
	t := &timer{
		h: r.NewHistogram(name, help, opts...).(*histogram),
	}
	// do not add metric, the underlying histogram is already added
	return t
}

func (r *Registry) NewLabeledTimer(name, help string, labels []string, opts ...metrics.Option) metrics.LabeledTimer {
	t := &labeledTimer{
		h: r.NewLabeledHistogram(name, help, labels, opts...).(*labeledHistogram),
	}
	// do not add metric, the underlying histogram is already added
	return t
}

func (r *Registry) NewHistogram(name, help string, opts ...metrics.Option) metrics.Histogram {
	t := &histogram{
		ph: prometheus.NewHistogram(r.newHistogramOpts(name, help, opts)),
	}
	r.add(t)
	return t
}

func (r *Registry) NewLabeledHistogram(name, help string, labels []string, opts ...metrics.Option) metrics.LabeledHistogram {
	t := &labeledHistogram{
		ph: prometheus.NewHistogramVec(r.newHistogramOpts(name, help, opts), labels),
	}
	r.add(t)
	return t
}

func (r *Registry) newHistogramOpts(name, help string, opts []metrics.Option) prometheus.HistogramOpts {
	return r.applyHistogramOptions(
		prometheus.HistogramOpts{
			Name:        name,
			Help:        help,
			ConstLabels: r.labels,
		},
		opts,
	)
}

func (r *Registry) applyHistogramOptions(promOpts prometheus.HistogramOpts, metricsOpts []metrics.Option) prometheus.HistogramOpts {
	for _, mopt := range metricsOpts {
		opt, ok := mopt.(option)
		if !ok {
			// skip non-prometheus options
			continue
		}
		promOpts = opt.(histogramOption).apply(promOpts)
	}
	return promOpts
}

func (r *Registry) Describe(ch chan<- *prometheus.Desc) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, metric := range r.metrics {
		metric.Describe(ch)
	}
}

func (r *Registry) Collect(ch chan<- prometheus.Metric) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, metric := range r.metrics {
		metric.Collect(ch)
	}
}

func (r *Registry) add(collector prometheus.Collector) {
	r.mu.Lock()
	r.metrics = append(r.metrics, collector)
	r.mu.Unlock()
}
