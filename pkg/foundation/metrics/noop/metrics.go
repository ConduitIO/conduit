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

// Package noop exposes implementations of metrics which do not do anything.
// These types are meant to be used in tests that do not care about metrics but
// need a non-nil reference.
package noop

import (
	"time"

	"github.com/conduitio/conduit/pkg/foundation/metrics"
)

type Counter struct{}

func (c Counter) Inc(vs ...float64) {}

type LabeledCounter struct{}

func (l LabeledCounter) WithValues(vs ...string) metrics.Counter { return Counter{} }

type Gauge struct{}

func (g Gauge) Inc(vs ...float64) {}
func (g Gauge) Dec(vs ...float64) {}
func (g Gauge) Set(f float64)     {}

type LabeledGauge struct{}

func (l LabeledGauge) WithValues(labels ...string) metrics.Gauge { return Gauge{} }

type Timer struct{}

func (t Timer) Update(duration time.Duration) {}
func (t Timer) UpdateSince(time time.Time)    {}

type LabeledTimer struct{}

func (l LabeledTimer) WithValues(labels ...string) metrics.Timer { return Timer{} }
