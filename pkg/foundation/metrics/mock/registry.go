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

package mock

import (
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/golang/mock/gomock"
)

// Registry is a metrics registry that can build mocked metrics.
type Registry struct {
	Ctrl *gomock.Controller

	SetupCounter        func(source *Counter)
	SetupGauge          func(source *Gauge)
	SetupTimer          func(source *Timer)
	SetupLabeledCounter func(source *LabeledCounter)
	SetupLabeledGauge   func(source *LabeledGauge)
	SetupLabeledTimer   func(source *LabeledTimer)
}

func (r Registry) NewCounter(name, help string) metrics.Counter {
	return NewCounter(r.Ctrl)
}

func (r Registry) NewGauge(name, help string) metrics.Gauge {
	return NewGauge(r.Ctrl)
}

func (r Registry) NewTimer(name, help string) metrics.Timer {
	return NewTimer(r.Ctrl)
}

func (r Registry) NewLabeledCounter(name, help string, labels ...string) metrics.LabeledCounter {
	return NewLabeledCounter(r.Ctrl)
}

func (r Registry) NewLabeledGauge(name, help string, labels ...string) metrics.LabeledGauge {
	return NewLabeledGauge(r.Ctrl)
}

func (r Registry) NewLabeledTimer(name, help string, labels ...string) metrics.LabeledTimer {
	return NewLabeledTimer(r.Ctrl)
}
