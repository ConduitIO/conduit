// Copyright © 2024 Meroxa, Inc.
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
	"context"
	"github.com/conduitio/conduit-commons/lang"
	"io"
	"sync/atomic"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

type ConfigurableSourcePlugin struct {
	*SourcePlugin
	Stream *builtin.InMemorySourceRunStream

	lastPosition atomic.Pointer[opencdc.Position]
	isStopped    atomic.Bool
	onRun        []func() error
}

// NewConfigurableSourcePlugin creates a mocked source plugin that can be
// configured using options.
func NewConfigurableSourcePlugin(
	ctrl *gomock.Controller,
	opts ...ConfigurableSourcePluginOption,
) *ConfigurableSourcePlugin {
	s := &ConfigurableSourcePlugin{
		SourcePlugin: NewSourcePlugin(ctrl),
	}
	for _, opt := range opts {
		opt.Apply(s)
	}
	return s
}

type ConfigurableSourcePluginOption interface {
	Apply(*ConfigurableSourcePlugin)
}

type configurableSourcePluginOptionFunc func(*ConfigurableSourcePlugin)

func (f configurableSourcePluginOptionFunc) Apply(p *ConfigurableSourcePlugin) { f(p) }

func SourcePluginWithConfigure() ConfigurableSourcePluginOption {
	return configurableSourcePluginOptionFunc(func(p *ConfigurableSourcePlugin) {
		p.EXPECT().
			Configure(gomock.Any(), gomock.Any()).
			Return(pconnector.SourceConfigureResponse{}, nil)
	})
}

func SourcePluginWithOpen() ConfigurableSourcePluginOption {
	return configurableSourcePluginOptionFunc(func(p *ConfigurableSourcePlugin) {
		p.EXPECT().
			Open(gomock.Any(), gomock.Any()).
			Return(pconnector.SourceOpenResponse{}, nil)
	})
}

func SourcePluginWithRun() ConfigurableSourcePluginOption {
	return configurableSourcePluginOptionFunc(func(p *ConfigurableSourcePlugin) {
		p.Stream = &builtin.InMemorySourceRunStream{}

		p.EXPECT().NewStream().Return(p.Stream)
		p.EXPECT().
			Run(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, _ pconnector.SourceRunStream) error {
				p.Stream.Init(ctx)
				if len(p.onRun) == 0 {
					// No other expectations for Run.
					return nil
				}

				// Run other expectations in parallel (they will generate
				// records and consume acks).
				for _, fn := range p.onRun {
					go func(fn func() error) {
						err := fn()
						if err != nil {
							p.Stream.Close(err)
						}
					}(fn)
				}

				return nil
			})
	})
}

func SourcePluginWithRecords(records []opencdc.Record, wantErr error) ConfigurableSourcePluginOption {
	return configurableSourcePluginOptionFunc(func(p *ConfigurableSourcePlugin) {
		t := p.ctrl.T.(*testing.T)
		is := is.New(t)

		var done atomic.Bool
		t.Cleanup(func() {
			is.True(done.Load()) // run didn't finish
		})

		p.onRun = append(p.onRun, func() error {
			defer done.Store(true)
			serverStream := p.Stream.Server()
			for _, rec := range records {
				err := serverStream.Send(pconnector.SourceRunResponse{Records: []opencdc.Record{rec}})
				if err != nil {
					return cerrors.Errorf("source mock send stream error: %w", err)
				}
				p.lastPosition.Store(&rec.Position)
				if p.isStopped.Load() {
					break
				}
			}
			return wantErr
		})
	})
}

func SourcePluginWithAcks(wantCount int, assertAckCount bool) ConfigurableSourcePluginOption {
	return configurableSourcePluginOptionFunc(func(p *ConfigurableSourcePlugin) {
		t := p.ctrl.T.(*testing.T)
		is := is.New(t)

		var gotCount atomic.Int64
		if assertAckCount {
			t.Cleanup(func() {
				is.Equal(int(gotCount.Load()), wantCount) // number of expected acks don't match
			})
		}

		p.onRun = append(p.onRun, func() error {
			serverStream := p.Stream.Server()
			for {
				_, err := serverStream.Recv()
				if err != nil {
					if cerrors.Is(err, context.Canceled) || cerrors.Is(err, io.EOF) {
						return nil // This is expected when the plugin is stopped.
					}
					return cerrors.Errorf("source mock recv stream error: %w", err)
				}
				gotCount.Add(1)
			}
		})
	})
}

func SourcePluginWithStop() ConfigurableSourcePluginOption {
	return configurableSourcePluginOptionFunc(func(p *ConfigurableSourcePlugin) {
		p.EXPECT().
			Stop(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, in pconnector.SourceStopRequest) (pconnector.SourceStopResponse, error) {
				p.isStopped.Store(true)
				lastPosition := p.lastPosition.Load()
				if lastPosition == nil {
					lastPosition = lang.Ptr(opencdc.Position{})
				}
				return pconnector.SourceStopResponse{LastPosition: *lastPosition}, nil
			})
	})
}

func SourcePluginWithTeardown() ConfigurableSourcePluginOption {
	return configurableSourcePluginOptionFunc(func(p *ConfigurableSourcePlugin) {
		p.EXPECT().
			Teardown(gomock.Any(), gomock.Any()).
			Return(pconnector.SourceTeardownResponse{}, nil)
	})
}
