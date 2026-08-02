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
	"sync"
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

// DestinationPluginWithUnorderedRecords is the concurrent-multi-source
// sibling of DestinationPluginWithRecords: it accepts the given records in
// ANY arrival order (matched by position, not by call order) and only
// requires that, by the time the mock's expectations are checked, every one
// of them was received exactly once with matching content.
//
// DestinationPluginWithRecords' strict positional match (records[offset])
// assumes a single, ordered producer. That assumption breaks the moment TWO
// sources share this destination (arch-v2 N-source pipelines): each source's
// batches arrive over the same stream, but WHICH source's batch lands first
// is a scheduling race with no defined winner - asserting a fixed order would
// make the test flaky, not the production code wrong. See
// pkg/lifecycle-poc's N×M shape-coverage tests, which need this to assert
// "every expected record arrived, from either source" without pinning an
// order neither the design nor the operator can rely on.
func DestinationPluginWithUnorderedRecords(records []opencdc.Record) ConfigurableDestinationPluginOption {
	return configurableDestinationPluginOptionFunc(func(p *ConfigurableDestinationPlugin) {
		t := p.ctrl.T.(*testing.T)
		is := is.New(t)

		var wg csync.WaitGroup
		wg.Add(1)

		want := make(map[string]opencdc.Record, len(records))
		for _, r := range records {
			want[string(r.Position)] = r
		}
		var mu sync.Mutex

		t.Cleanup(func() {
			err := wg.WaitTimeout(context.Background(), time.Second)
			is.NoErr(err) // run didn't finish

			mu.Lock()
			remaining := len(want)
			mu.Unlock()
			is.Equal(0, remaining) // every expected record must have arrived exactly once
		})

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

				acks := make([]pconnector.DestinationRunResponseAck, len(req.Records))
				mu.Lock()
				for i, got := range req.Records {
					wantRec, ok := want[string(got.Position)]
					if !ok {
						mu.Unlock()
						return cerrors.Errorf("destination mock received an unexpected or duplicate record at position %q", got.Position)
					}
					is.Equal(got, wantRec)
					delete(want, string(got.Position))
					acks[i] = pconnector.DestinationRunResponseAck{Position: got.Position}
				}
				mu.Unlock()

				if err := serverStream.Send(pconnector.DestinationRunResponse{Acks: acks}); err != nil {
					return cerrors.Errorf("destination mock send stream error: %w", err)
				}
			}
		})
	})
}

// DestinationPluginWithControlledError builds a destination plugin that
// receives exactly len(records) records (asserting they match, like
// DestinationPluginWithRecords), signals received once every record has been
// received (but NOT yet acked), then blocks until release is closed, and
// finally returns wantErr instead of ever sending an ack response.
//
// This exists for deterministically testing the "operator Stop races a
// transient drain error" class of scenario (see
// pkg/lifecycle-poc.TestServiceLifecycle_Stop_TransientErrorMidDrain_NoRecovery):
// unlike DestinationPluginWithRecords (which always acks successfully and
// offers no hook to control exactly when a failure surfaces relative to a
// concurrent Stop call), this lets a test hold the batch "in flight" (and
// thus hold funnel.Worker's processingLock) until it has confirmed some other
// condition (e.g. that Stop has already been invoked), only then releasing
// the failure. wantErr must be non-nil (a nil wantErr would leave the stream
// open forever with no further sends, which is a "block forever" behavior a
// caller should get via a different, dedicated helper instead of overloading
// this one with two meanings for the zero value).
func DestinationPluginWithControlledError(records []opencdc.Record, received chan<- struct{}, release <-chan struct{}, wantErr error) ConfigurableDestinationPluginOption {
	return configurableDestinationPluginOptionFunc(func(p *ConfigurableDestinationPlugin) {
		t := p.ctrl.T.(*testing.T)
		is := is.New(t)
		if wantErr == nil {
			t.Fatalf("DestinationPluginWithControlledError: wantErr must be non-nil")
		}

		p.onRun = append(p.onRun, func() error {
			serverStream := p.Stream.Server()

			offset := 0
			for offset < len(records) {
				req, err := serverStream.Recv()
				if err != nil {
					if cerrors.Is(err, context.Canceled) || cerrors.Is(err, io.EOF) {
						return nil // stopped/torn down before the controlled error ever fired
					}
					return cerrors.Errorf("destination mock recv stream error: %w", err)
				}
				for _, got := range req.Records {
					if offset >= len(records) {
						return cerrors.Errorf("destination mock received more records than expected")
					}
					is.Equal(got, records[offset])
					offset++
				}
			}

			close(received)
			<-release
			return wantErr
		})
	})
}

// DestinationPluginWithControlledBlock is the clean-release sibling of
// DestinationPluginWithControlledError: it holds the batch "in flight" (and thus
// holds funnel.Worker's processingLock) — signaling on received once it has the
// records and blocking on release — then acks them SUCCESSFULLY and returns nil,
// rather than surfacing an error. Used to deterministically sequence a
// concurrent Stop against a still-in-flight-but-healthy batch (e.g. so the
// drain-terminating error can come from a DIFFERENT source, like a failing
// source Teardown, without the destination itself erroring — see
// pkg/lifecycle-poc.TestServiceLifecycle_Stop_SourceTeardownFails_NoRecovery).
func DestinationPluginWithControlledBlock(records []opencdc.Record, received chan<- struct{}, release <-chan struct{}) ConfigurableDestinationPluginOption {
	return configurableDestinationPluginOptionFunc(func(p *ConfigurableDestinationPlugin) {
		is := is.New(p.ctrl.T.(*testing.T))

		p.onRun = append(p.onRun, func() error {
			serverStream := p.Stream.Server()

			offset := 0
			for offset < len(records) {
				req, err := serverStream.Recv()
				if err != nil {
					if cerrors.Is(err, context.Canceled) || cerrors.Is(err, io.EOF) {
						return nil // stopped/torn down before we finished receiving
					}
					return cerrors.Errorf("destination mock recv stream error: %w", err)
				}
				acks := make([]pconnector.DestinationRunResponseAck, len(req.Records))
				for i, got := range req.Records {
					if offset >= len(records) {
						return cerrors.Errorf("destination mock received more records than expected")
					}
					is.Equal(got, records[offset])
					acks[i] = pconnector.DestinationRunResponseAck{Position: got.Position}
					offset++
				}

				close(received)
				<-release // hold the batch in flight (processingLock held) until released

				if err := serverStream.Send(pconnector.DestinationRunResponse{Acks: acks}); err != nil {
					return cerrors.Errorf("destination mock send ack error: %w", err)
				}
			}

			// Drain any further recv until the stream is stopped/torn down.
			for {
				_, err := serverStream.Recv()
				if err != nil {
					if cerrors.Is(err, context.Canceled) || cerrors.Is(err, io.EOF) {
						return nil
					}
					return cerrors.Errorf("destination mock recv stream error: %w", err)
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
