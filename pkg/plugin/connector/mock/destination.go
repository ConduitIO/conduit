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
	"io"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

type ConfigurableDestinationPlugin struct {
	*DestinationPlugin
	Stream *builtin.InMemoryDestinationRunStream

	onRun []func() error
}

// NewConfigurableDestinationPlugin creates a mocked destination plugin that can be
// configured using options.
func NewConfigurableDestinationPlugin(
	ctrl *gomock.Controller,
	opts ...ConfigurableDestinationPluginOption,
) *ConfigurableDestinationPlugin {
	d := &ConfigurableDestinationPlugin{
		DestinationPlugin: NewDestinationPlugin(ctrl),
	}
	for _, opt := range opts {
		opt.Apply(d)
	}
	return d
}

type ConfigurableDestinationPluginOption interface {
	Apply(*ConfigurableDestinationPlugin)
}

type configurableDestinationPluginOptionFunc func(*ConfigurableDestinationPlugin)

func (f configurableDestinationPluginOptionFunc) Apply(p *ConfigurableDestinationPlugin) { f(p) }

func DestinationPluginWithConfigure() ConfigurableDestinationPluginOption {
	return configurableDestinationPluginOptionFunc(func(p *ConfigurableDestinationPlugin) {
		p.EXPECT().
			Configure(gomock.Any(), gomock.Any()).
			Return(pconnector.DestinationConfigureResponse{}, nil)
	})
}

func DestinationPluginWithOpen() ConfigurableDestinationPluginOption {
	return configurableDestinationPluginOptionFunc(func(p *ConfigurableDestinationPlugin) {
		p.EXPECT().
			Open(gomock.Any(), gomock.Any()).
			Return(pconnector.DestinationOpenResponse{}, nil)
	})
}

func DestinationPluginWithRun() ConfigurableDestinationPluginOption {
	return configurableDestinationPluginOptionFunc(func(p *ConfigurableDestinationPlugin) {
		p.Stream = &builtin.InMemoryDestinationRunStream{}

		p.EXPECT().NewStream().Return(p.Stream)
		p.EXPECT().
			Run(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, _ pconnector.DestinationRunStream) error {
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

func DestinationPluginWithRecords(records []opencdc.Record) ConfigurableDestinationPluginOption {
	return configurableDestinationPluginOptionFunc(func(p *ConfigurableDestinationPlugin) {
		t := p.ctrl.T.(*testing.T)
		is := is.New(t)

		var wg csync.WaitGroup
		wg.Add(1)
		t.Cleanup(func() {
			err := wg.WaitTimeout(context.Background(), time.Second)
			is.NoErr(err) // run didn't finish
		})

		offset := 0
		p.onRun = append(p.onRun, func() error {
			defer wg.Done()
			serverStream := p.Stream.Server()

			for {
				req, err := serverStream.Recv()
				if err != nil {
					if cerrors.Is(err, context.Canceled) || cerrors.Is(err, io.EOF) {
						return nil // This is expected when the plugin is stopped.
					}
					return cerrors.Errorf("destination mock recv stream error: %w", err)
				}

				is.NoErr(err)

				acks := make([]pconnector.DestinationRunResponseAck, len(req.Records))
				for i, got := range req.Records {
					if offset >= len(records) {
						return cerrors.Errorf("destination mock received more records than expected")
					}
					is.Equal(got, records[offset])
					offset++
					acks[i] = pconnector.DestinationRunResponseAck{Position: got.Position}
				}

				err = serverStream.Send(pconnector.DestinationRunResponse{Acks: acks})
				if err != nil {
					return cerrors.Errorf("destination mock send stream error: %w", err)
				}
			}
		})
	})
}

func DestinationPluginWithStop(lastPosition opencdc.Position) ConfigurableDestinationPluginOption {
	return configurableDestinationPluginOptionFunc(func(p *ConfigurableDestinationPlugin) {
		is := is.New(p.ctrl.T.(*testing.T))
		p.EXPECT().
			Stop(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, in pconnector.DestinationStopRequest) (pconnector.DestinationStopResponse, error) {
				is.Equal(lastPosition, in.LastPosition)
				return pconnector.DestinationStopResponse{}, nil
			})
	})
}

func DestinationPluginWithTeardown() ConfigurableDestinationPluginOption {
	return configurableDestinationPluginOptionFunc(func(p *ConfigurableDestinationPlugin) {
		p.EXPECT().
			Teardown(gomock.Any(), gomock.Any()).
			Return(pconnector.DestinationTeardownResponse{}, nil)
	})
}
