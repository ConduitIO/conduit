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
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/golang/mock/gomock"
)

// Builder is a builder that can build destination and source mocks.
type Builder struct {
	Ctrl             *gomock.Controller
	SetupSource      func(source *Source)
	SetupDestination func(source *Destination)
}

func (b Builder) Build(t connector.Type) (connector.Connector, error) {
	switch t {
	case connector.TypeSource:
		return NewSource(b.Ctrl), nil
	case connector.TypeDestination:
		return NewDestination(b.Ctrl), nil
	}
	return nil, connector.ErrInvalidConnectorType
}

func (b Builder) Init(c connector.Connector, id string, cfg connector.Config) error {
	switch m := c.(type) {
	case *Source:
		m.ctrl = b.Ctrl
		m.recorder = &SourceMockRecorder{m}
		m.EXPECT().Type().Return(connector.TypeSource).AnyTimes()
		m.EXPECT().ID().Return(id).AnyTimes()
		m.EXPECT().Config().Return(cfg).AnyTimes()
		if b.SetupSource != nil {
			b.SetupSource(m)
		}
	case *Destination:
		m.ctrl = b.Ctrl
		m.recorder = &DestinationMockRecorder{m}
		m.EXPECT().Type().Return(connector.TypeDestination).AnyTimes()
		m.EXPECT().ID().Return(id).AnyTimes()
		m.EXPECT().Config().Return(cfg).AnyTimes()
		if b.SetupDestination != nil {
			b.SetupDestination(m)
		}
	default:
		return connector.ErrInvalidConnectorType
	}
	return nil
}

func (b Builder) NewDestinationMock(id string, d connector.Config) *Destination {
	m := NewDestination(b.Ctrl)
	m.EXPECT().Type().Return(connector.TypeDestination).AnyTimes()
	m.EXPECT().ID().Return(id).AnyTimes()
	m.EXPECT().Config().Return(d).AnyTimes()
	if b.SetupDestination != nil {
		b.SetupDestination(m)
	}
	return m
}

func (b Builder) NewSourceMock(id string, d connector.Config) *Source {
	m := NewSource(b.Ctrl)
	m.EXPECT().Type().Return(connector.TypeSource).AnyTimes()
	m.EXPECT().ID().Return(id).AnyTimes()
	m.EXPECT().Config().Return(d).AnyTimes()
	if b.SetupSource != nil {
		b.SetupSource(m)
	}
	return m
}
