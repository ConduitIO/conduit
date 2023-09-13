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

func TestCounter(t *testing.T) {
	is := is.New(t)

	testCases := []struct {
		name      string
		observe   func(m metrics.Counter)
		wantValue float64
	}{{
		name:      "empty counter",
		observe:   func(m metrics.Counter) {},
		wantValue: 0,
	}, {
		name:      "increment once",
		observe:   func(m metrics.Counter) { m.Inc() },
		wantValue: 1,
	}, {
		name: "increment 10 times",
		observe: func(m metrics.Counter) {
			for i := 0; i < 10; i++ {
				m.Inc()
			}
		},
		wantValue: 10,
	}, {
		name:      "increment integer",
		observe:   func(m metrics.Counter) { m.Inc(123) },
		wantValue: 123,
	}, {
		name:      "increment float",
		observe:   func(m metrics.Counter) { m.Inc(1.23) },
		wantValue: 1.23,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry(nil)
			m := reg.NewCounter("my_counter", "test counter")
			tc.observe(m)

			mt := dto.MetricType_COUNTER
			want := []*dto.MetricFamily{{
				Name: proto.String("my_counter"),
				Help: proto.String("test counter"),
				Type: &mt,
				Metric: []*dto.Metric{{
					Label: make([]*dto.LabelPair, 0),
					Counter: &dto.Counter{
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

func TestCounter_IncNegative(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected negative increment to panic")
		}
	}()

	reg := NewRegistry(nil)
	m := reg.NewCounter("my_counter", "test counter")
	m.Inc(-1)
}

func TestLabeledCounter(t *testing.T) {
	is := is.New(t)

	testCases := []struct {
		name       string
		observe    func(m metrics.LabeledCounter)
		wantValues map[string]float64
	}{{
		name:       "no observed counters",
		observe:    func(m metrics.LabeledCounter) {},
		wantValues: nil,
	}, {
		name:       "only labels observed",
		observe:    func(m metrics.LabeledCounter) { m.WithValues("val1") },
		wantValues: map[string]float64{"val1": 0},
	}, {
		name: "one observed",
		observe: func(m metrics.LabeledCounter) {
			m1 := m.WithValues("val1")
			m1.Inc()
			m1.Inc(1.23)
		},
		wantValues: map[string]float64{"val1": 2.23},
	}, {
		name: "10 observed",
		observe: func(m metrics.LabeledCounter) {
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
			m := reg.NewLabeledCounter("my_labeled_counter", "test labeled counter", []string{"test_label"})
			tc.observe(m)

			mt := dto.MetricType_COUNTER
			want := make([]*dto.MetricFamily, 0)
			if len(tc.wantValues) > 0 {
				mf := &dto.MetricFamily{
					Name:   proto.String("my_labeled_counter"),
					Help:   proto.String("test labeled counter"),
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
						Counter: &dto.Counter{
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
