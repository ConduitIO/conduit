// Copyright © 2026 Meroxa, Inc.
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

package lifecycle

import (
	"context"
	"io"
	"sync/atomic"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"go.uber.org/mock/gomock"
)

// sdkBatchingDestination returns a destination connector instance, carrying
// settings as its engine-stored config, whose plugin behaves like a
// conduit-connector-sdk destination with sdk.batch.size set: a write only
// enqueues, and acks for the queued records are sent once the queue reaches
// batchSize. There is deliberately no time-based flush, which is exactly what
// the SDK does with sdk.batch.delay == 0 (#2979).
//
// batchSize <= 1 acks every write immediately (the SDK's no-batching path).
// written counts records the plugin received.
func sdkBatchingDestination(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	settings map[string]string,
	batchSize int,
) (*connector.Instance, *pmock.Dispenser, *atomic.Int64) {
	var written atomic.Int64
	plugin := pmock.NewDestinationPlugin(ctrl)
	stream := &builtin.InMemoryDestinationRunStream{}

	plugin.EXPECT().Configure(gomock.Any(), gomock.Any()).Return(pconnector.DestinationConfigureResponse{}, nil).AnyTimes()
	plugin.EXPECT().Open(gomock.Any(), gomock.Any()).Return(pconnector.DestinationOpenResponse{}, nil).AnyTimes()
	plugin.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).Return(pconnector.DestinationLifecycleOnCreatedResponse{}, nil).AnyTimes()
	plugin.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.DestinationTeardownResponse{}, nil).AnyTimes()
	plugin.EXPECT().NewStream().Return(stream).AnyTimes()
	plugin.EXPECT().Run(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ pconnector.DestinationRunStream) error {
			stream.Init(ctx)
			go func() {
				server := stream.Server()
				var queued []opencdc.Position
				for {
					req, err := server.Recv()
					if err != nil {
						if !cerrors.Is(err, context.Canceled) && !cerrors.Is(err, io.EOF) {
							stream.Close(err)
						}
						return
					}
					for _, rec := range req.Records {
						written.Add(1)
						queued = append(queued, rec.Position)
					}
					if len(queued) < batchSize && batchSize > 1 {
						continue // SDK Write only enqueues; no ack until the flush.
					}
					acks := make([]pconnector.DestinationRunResponseAck, len(queued))
					for i, p := range queued {
						acks[i] = pconnector.DestinationRunResponseAck{Position: p}
					}
					queued = queued[:0]
					if err := server.Send(pconnector.DestinationRunResponse{Acks: acks}); err != nil {
						return
					}
				}
			}()
			return nil
		}).AnyTimes()

	dest := dummyDestination(persister)
	dest.Config = connector.Config{Name: "batching-destination", Settings: settings}

	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseDestination().Return(plugin, nil).AnyTimes()
	return dest, dispenser, &written
}

// untouchedDispenser returns a dispenser that fails the test if anything
// dispenses a plugin from it: the proof that a refused start opened nothing.
func untouchedDispenser(ctrl *gomock.Controller) *pmock.Dispenser {
	return pmock.NewDispenser(ctrl) // no expectations: any call is a test failure
}

// oneRecordSource is a source plugin that emits records one per response (the
// SDK default read) and tolerates any number of acks.
func oneRecordSource(
	ctrl *gomock.Controller,
	persister *connector.Persister,
	records []opencdc.Record,
) (*connector.Instance, *pmock.Dispenser) {
	plugin := pmock.NewConfigurableSourcePlugin(ctrl,
		pmock.SourcePluginWithConfigure(),
		pmock.SourcePluginWithOpen(),
		pmock.SourcePluginWithRun(),
		pmock.SourcePluginWithRecords(records, nil),
		pmock.SourcePluginWithAcks(len(records), false),
		pmock.SourcePluginWithTeardown(),
	)
	source := dummySource(persister)
	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().Return(plugin, nil)
	return source, dispenser
}
