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

import "github.com/prometheus/client_golang/prometheus"

type option interface {
	prometheusOption()
}
type gaugeOption interface {
	option
	apply(prometheus.GaugeOpts) prometheus.GaugeOpts
}
type histogramOption interface {
	option
	apply(prometheus.HistogramOpts) prometheus.HistogramOpts
}
type counterOption interface {
	option
	apply(prometheus.CounterOpts) prometheus.CounterOpts
}

type HistogramOpts struct {
	Buckets []float64
}

func (o HistogramOpts) prometheusOption() {}
func (o HistogramOpts) apply(opts prometheus.HistogramOpts) prometheus.HistogramOpts {
	if o.Buckets != nil {
		opts.Buckets = o.Buckets
	}
	return opts
}
