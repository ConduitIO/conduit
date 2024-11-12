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

package metrics

import (
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
)

// Registry is an object that can create and collect metrics.
type Registry interface {
	NewCounter(name, help string, opts ...Option) Counter
	NewGauge(name, help string, opts ...Option) Gauge
	NewTimer(name, help string, opts ...Option) Timer
	NewHistogram(name, help string, opts ...Option) Histogram

	NewLabeledCounter(name, help string, labels []string, opts ...Option) LabeledCounter
	NewLabeledGauge(name, help string, labels []string, opts ...Option) LabeledGauge
	NewLabeledTimer(name, help string, labels []string, opts ...Option) LabeledTimer
	NewLabeledHistogram(name, help string, labels []string, opts ...Option) LabeledHistogram
}

// Option is an option that can be applied on a metric. Registry implementations
// can and should define their own unique Option interface and only apply
// options meant for them.
type Option interface{}

// Counter is a metric that can only increment its current count.
type Counter interface {
	// Inc adds Sum(vs) to the counter. Sum(vs) must be positive.
	//
	// If len(vs) == 0, increments the counter by 1.
	Inc(vs ...float64)
}

// LabeledCounter is a counter that must have labels populated before use.
type LabeledCounter interface {
	WithValues(vs ...string) Counter
}

// Gauge is a metric that allows incrementing and decrementing a value.
type Gauge interface {
	// Inc adds Sum(vs) to the gauge. Sum(vs) must be positive.
	//
	// If len(vs) == 0, increments the gauge by 1.
	Inc(vs ...float64)
	// Dec subtracts Sum(vs) from the gauge. Sum(vs) must be positive.
	//
	// If len(vs) == 0, decrements the gauge by 1.
	Dec(vs ...float64)

	// Set replaces the gauge's current value with the provided value
	Set(float64)
}

// LabeledGauge describes a gauge that must have values populated before use.
type LabeledGauge interface {
	// WithValues returns the Gauge for the given slice of label
	// values (same order as the label names used when creating this LabeledGauge).
	// If that combination of label values is accessed for the first time,
	// a new Gauge is created.
	WithValues(labels ...string) Gauge
}

// Timer is a metric that allows collecting the duration of an action in
// seconds.
type Timer interface {
	// Update records a duration.
	Update(time.Duration)

	// UpdateSince will add the duration from the provided starting time to the
	// timer's summary.
	UpdateSince(time.Time)
}

// LabeledTimer is a timer that must have label values populated before use.
type LabeledTimer interface {
	WithValues(labels ...string) Timer
}

// Histogram is a metric that builds a histogram from observed values.
type Histogram interface {
	Observe(float64)
}

// LabeledHistogram describes a histogram that must have labels populated before
// use.
type LabeledHistogram interface {
	WithValues(labels ...string) Histogram
}

var global = struct {
	metrics    []metric
	registries []Registry
}{}

// Register adds a Registry to the global registries. Any metrics that were
// created prior or after this call will also be created in this registry. This
// function is not thread safe, registries should be registered either before
// or after creating metrics, but not at the same time.
func Register(r Registry) {
	global.registries = append(global.registries, r)
	for _, mt := range global.metrics {
		mt.New(r)
	}
}

func NewCounter(name, help string, opts ...Option) Counter {
	mt := &counter{
		spec: spec{
			name: name,
			help: help,
			opts: opts,
		},
	}
	addMetric(mt)
	return mt
}

func NewGauge(name, help string, opts ...Option) Gauge {
	mt := &gauge{
		spec: spec{
			name: name,
			help: help,
			opts: opts,
		},
	}
	addMetric(mt)
	return mt
}

func NewTimer(name, help string, opts ...Option) Timer {
	mt := &timer{
		spec: spec{
			name: name,
			help: help,
			opts: opts,
		},
	}
	addMetric(mt)
	return mt
}

func NewHistogram(name, help string, opts ...Option) Histogram {
	mt := &histogram{
		spec: spec{
			name: name,
			help: help,
			opts: opts,
		},
	}
	addMetric(mt)
	return mt
}

func NewLabeledCounter(name, help string, labels []string, opts ...Option) LabeledCounter {
	mt := &labeledCounter{
		spec: spec{
			name:   name,
			help:   help,
			labels: labels,
			opts:   opts,
		},
	}
	addMetric(mt)
	return mt
}

func NewLabeledGauge(name, help string, labels []string, opts ...Option) LabeledGauge {
	mt := &labeledGauge{
		spec: spec{
			name:   name,
			help:   help,
			labels: labels,
			opts:   opts,
		},
	}
	addMetric(mt)
	return mt
}

func NewLabeledTimer(name, help string, labels []string, opts ...Option) LabeledTimer {
	mt := &labeledTimer{
		spec: spec{
			name:   name,
			help:   help,
			labels: labels,
			opts:   opts,
		},
	}
	addMetric(mt)
	return mt
}

func NewLabeledHistogram(name, help string, labels []string, opts ...Option) LabeledHistogram {
	mt := &labeledHistogram{
		spec: spec{
			name:   name,
			help:   help,
			labels: labels,
			opts:   opts,
		},
	}
	addMetric(mt)
	return mt
}

func addMetric(mt metric) {
	global.metrics = append(global.metrics, mt)
	for _, r := range global.registries {
		mt.New(r)
	}
}

type metric interface {
	New(Registry)
}

type spec struct {
	name   string
	help   string
	labels []string
	opts   []Option
}

type counter struct {
	spec
	metrics []Counter
}

func (mt *counter) New(r Registry) {
	m := r.NewCounter(mt.name, mt.help, mt.opts...)
	mt.metrics = append(mt.metrics, m)
}

func (mt *counter) Inc(vs ...float64) {
	for _, m := range mt.metrics {
		m.Inc(vs...)
	}
}

type labeledCounter struct {
	spec
	metrics []LabeledCounter
}

func (mt *labeledCounter) New(r Registry) {
	m := r.NewLabeledCounter(mt.name, mt.help, mt.labels, mt.opts...)
	mt.metrics = append(mt.metrics, m)
}

func (mt *labeledCounter) WithValues(vs ...string) Counter {
	c := &counter{
		spec:    mt.spec,
		metrics: make([]Counter, len(mt.metrics)),
	}
	for i, m := range mt.metrics {
		c.metrics[i] = m.WithValues(vs...)
	}
	return c
}

type gauge struct {
	spec
	metrics []Gauge
}

func (mt *gauge) New(r Registry) {
	m := r.NewGauge(mt.name, mt.help, mt.opts...)
	mt.metrics = append(mt.metrics, m)
}

func (mt *gauge) Inc(f ...float64) {
	for _, m := range mt.metrics {
		m.Inc(f...)
	}
}

func (mt *gauge) Dec(f ...float64) {
	for _, m := range mt.metrics {
		m.Dec(f...)
	}
}

func (mt *gauge) Set(f float64) {
	for _, m := range mt.metrics {
		m.Set(f)
	}
}

type labeledGauge struct {
	spec
	metrics []LabeledGauge
}

func (mt *labeledGauge) New(r Registry) {
	m := r.NewLabeledGauge(mt.name, mt.help, mt.labels, mt.opts...)
	mt.metrics = append(mt.metrics, m)
}

func (mt *labeledGauge) WithValues(vs ...string) Gauge {
	g := &gauge{
		spec:    mt.spec,
		metrics: make([]Gauge, len(mt.metrics)),
	}
	for i, m := range mt.metrics {
		g.metrics[i] = m.WithValues(vs...)
	}
	return g
}

type timer struct {
	spec
	metrics []Timer
}

func (mt *timer) New(r Registry) {
	m := r.NewTimer(mt.name, mt.help, mt.opts...)
	mt.metrics = append(mt.metrics, m)
}

func (mt *timer) Update(d time.Duration) {
	for _, m := range mt.metrics {
		m.Update(d)
	}
}

func (mt *timer) UpdateSince(t time.Time) {
	for _, m := range mt.metrics {
		m.UpdateSince(t)
	}
}

type labeledTimer struct {
	spec
	metrics []LabeledTimer
}

func (mt *labeledTimer) New(r Registry) {
	m := r.NewLabeledTimer(mt.name, mt.help, mt.labels, mt.opts...)
	mt.metrics = append(mt.metrics, m)
}

func (mt *labeledTimer) WithValues(vs ...string) Timer {
	t := &timer{
		spec:    mt.spec,
		metrics: make([]Timer, len(mt.metrics)),
	}
	for i, m := range mt.metrics {
		t.metrics[i] = m.WithValues(vs...)
	}
	return t
}

type histogram struct {
	spec
	metrics []Histogram
}

func (mt *histogram) New(r Registry) {
	m := r.NewHistogram(mt.name, mt.help, mt.opts...)
	mt.metrics = append(mt.metrics, m)
}

func (mt *histogram) Observe(v float64) {
	for _, m := range mt.metrics {
		m.Observe(v)
	}
}

type labeledHistogram struct {
	spec
	metrics []LabeledHistogram
}

func (mt *labeledHistogram) New(r Registry) {
	m := r.NewLabeledHistogram(mt.name, mt.help, mt.labels, mt.opts...)
	mt.metrics = append(mt.metrics, m)
}

func (mt *labeledHistogram) WithValues(vs ...string) Histogram {
	t := &histogram{
		spec:    mt.spec,
		metrics: make([]Histogram, len(mt.metrics)),
	}
	for i, m := range mt.metrics {
		t.metrics[i] = m.WithValues(vs...)
	}
	return t
}

// RecordBytesHistogram wraps a histrogram metric and allows to observe record
// sizes in bytes.
type RecordBytesHistogram struct {
	H Histogram
}

func NewRecordBytesHistogram(h Histogram) RecordBytesHistogram {
	return RecordBytesHistogram{H: h}
}

func (m RecordBytesHistogram) Observe(r opencdc.Record) {
	m.H.Observe(m.SizeOf(r))
}

func (m RecordBytesHistogram) SizeOf(r opencdc.Record) float64 {
	// TODO for now we call method Bytes() on key and payload to get the
	//  bytes representation. In case of a structured payload or key it
	//  is marshaled into JSON, which might not be the correct way to
	//  determine bytes. Not sure how we could improve this part without
	//  offloading the bytes calculation to the plugin.

	var bytes int
	if r.Key != nil {
		bytes += len(r.Key.Bytes())
	}
	if r.Payload.Before != nil {
		bytes += len(r.Payload.Before.Bytes())
	}
	if r.Payload.After != nil {
		bytes += len(r.Payload.After.Bytes())
	}
	return float64(bytes)
}
