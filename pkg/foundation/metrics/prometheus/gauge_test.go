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
	"fmt"
	"sort"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/matryer/is"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/protobuf/proto"
)

func TestGauge(t *testing.T) {
	is := is.New(t)

	testCases := []struct {
		name      string
		observe   func(m metrics.Gauge)
		wantValue float64
	}{{
		name:      "empty gauge",
		observe:   func(m metrics.Gauge) {},
		wantValue: 0,
	}, {
		name:      "increment once",
		observe:   func(m metrics.Gauge) { m.Inc() },
		wantValue: 1,
	}, {
		name: "increment 10 times",
		observe: func(m metrics.Gauge) {
			for i := 0; i < 10; i++ {
				m.Inc()
			}
		},
		wantValue: 10,
	}, {
		name:      "increment integer",
		observe:   func(m metrics.Gauge) { m.Inc(123) },
		wantValue: 123,
	}, {
		name:      "increment float",
		observe:   func(m metrics.Gauge) { m.Inc(1.23) },
		wantValue: 1.23,
	}, {
		name:      "increment negative",
		observe:   func(m metrics.Gauge) { m.Inc(-1.23) },
		wantValue: -1.23,
	}, {
		name:      "decrement once",
		observe:   func(m metrics.Gauge) { m.Dec() },
		wantValue: -1,
	}, {
		name: "decrement 10 times",
		observe: func(m metrics.Gauge) {
			for i := 0; i < 10; i++ {
				m.Dec()
			}
		},
		wantValue: -10,
	}, {
		name:      "decrement integer",
		observe:   func(m metrics.Gauge) { m.Dec(123) },
		wantValue: -123,
	}, {
		name:      "decrement float",
		observe:   func(m metrics.Gauge) { m.Dec(1.23) },
		wantValue: -1.23,
	}, {
		name:      "decrement negative",
		observe:   func(m metrics.Gauge) { m.Dec(-1.23) },
		wantValue: 1.23,
	}, {
		name: "set",
		observe: func(m metrics.Gauge) {
			m.Inc() // increment should not matter
			m.Set(1.23)
		},
		wantValue: 1.23,
	}, {
		name: "all at once",
		observe: func(m metrics.Gauge) {
			m.Set(123)
			m.Inc(1.23)
			m.Dec()
		},
		wantValue: 123.23,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry(nil)
			m := reg.NewGauge("my_gauge", "test gauge")
			tc.observe(m)

			mt := dto.MetricType_GAUGE
			want := []*dto.MetricFamily{{
				Name: proto.String("my_gauge"),
				Help: proto.String("test gauge"),
				Type: &mt,
				Metric: []*dto.Metric{{
					Label: make([]*dto.LabelPair, 0),
					Gauge: &dto.Gauge{
						Value: proto.Float64(tc.wantValue),
					},
				}},
			}}

			promRegistry := prometheus.NewRegistry()
			err := promRegistry.Register(reg)
			is.NoErr(err)

			got, err := promRegistry.Gather()
			is.NoErr(err)
			is.Equal(want, got)
		})
	}
}

func TestLabeledGauge(t *testing.T) {
	is := is.New(t)

	testCases := []struct {
		name       string
		observe    func(m metrics.LabeledGauge)
		wantValues map[string]float64
	}{{
		name:       "no observed gauges",
		observe:    func(m metrics.LabeledGauge) {},
		wantValues: nil,
	}, {
		name:       "only labels observed",
		observe:    func(m metrics.LabeledGauge) { m.WithValues("val1") },
		wantValues: map[string]float64{"val1": 0},
	}, {
		name: "one observed",
		observe: func(m metrics.LabeledGauge) {
			m1 := m.WithValues("val1")
			m1.Inc()
			m1.Dec(2.1)
		},
		wantValues: map[string]float64{"val1": -1.1},
	}, {
		name: "10 observed",
		observe: func(m metrics.LabeledGauge) {
			for i := 1; i <= 10; i++ {
				m1 := m.WithValues(fmt.Sprintf("val%d", i))
				m1.Inc(float64(i))
			}
		},
		wantValues: map[string]float64{
			"val1": 1, "val2": 2, "val3": 3, "val4": 4, "val5": 5, "val6": 6, "val7": 7, "val8": 8, "val9": 9, "val10": 10,
		},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry(nil)
			m := reg.NewLabeledGauge("my_labeled_gauge", "test labeled gauge", []string{"test_label"})
			tc.observe(m)

			mt := dto.MetricType_GAUGE
			want := make([]*dto.MetricFamily, 0)
			if len(tc.wantValues) > 0 {
				mf := &dto.MetricFamily{
					Name:   proto.String("my_labeled_gauge"),
					Help:   proto.String("test labeled gauge"),
					Type:   &mt,
					Metric: []*dto.Metric{},
				}

				// iterate through map in an ordered way
				keys := make([]string, 0)
				for k := range tc.wantValues {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, label := range keys {
					mf.Metric = append(mf.Metric, &dto.Metric{
						Label: []*dto.LabelPair{{
							Name:  proto.String("test_label"),
							Value: proto.String(label),
						}},
						Gauge: &dto.Gauge{
							Value: proto.Float64(tc.wantValues[label]),
						},
					})
				}
				want = append(want, mf)
			}

			promRegistry := prometheus.NewRegistry()
			err := promRegistry.Register(reg)
			is.NoErr(err)

			got, err := promRegistry.Gather()
			is.NoErr(err)
			is.Equal(want, got)
		})
	}
}
