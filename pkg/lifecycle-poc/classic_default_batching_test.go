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
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// builtinRecorder looks like the built-in connector called name and records
// the settings its destination plugins are configured with.
type builtinRecorder struct {
	connectorPlugin.Dispenser
	name string

	mu      sync.Mutex
	configs []map[string]string
}

func (r *builtinRecorder) BuiltinPluginName() (string, bool) { return r.name, true }

func (r *builtinRecorder) DispenseDestination() (connectorPlugin.DestinationPlugin, error) {
	p, err := r.Dispenser.DispenseDestination()
	if err != nil {
		return nil, err
	}
	return &recordingDest{DestinationPlugin: p, r: r}, nil
}

type recordingDest struct {
	connectorPlugin.DestinationPlugin
	r *builtinRecorder
}

func (d *recordingDest) Configure(ctx context.Context, req pconnector.DestinationConfigureRequest) (pconnector.DestinationConfigureResponse, error) {
	cp := map[string]string{}
	for k, v := range req.Config {
		cp[k] = v
	}
	d.r.mu.Lock()
	d.r.configs = append(d.r.configs, cp)
	d.r.mu.Unlock()
	return d.DestinationPlugin.Configure(ctx, req)
}

// TestStart_ArchV2NeverGetsClassicDefaultBatching is the regression test for
// the classic engine's default destination batching
// (docs/design-documents/20261020-classic-default-destination-batching.md):
// arch-v2 batches in the funnel, so a built-in postgres or kafka destination
// with no batching settings must reach Configure without sdk.batch.size or
// sdk.batch.delay. With the classic default leaking in, the SDK would batch
// underneath the funnel's stop-and-wait loop and withhold acks for up to the
// delay on every write.
func TestStart_ArchV2NeverGetsClassicDefaultBatching(t *testing.T) {
	for _, builtinName := range []string{"postgres", "kafka"} {
		t.Run(builtinName, func(t *testing.T) {
			is := is.New(t)
			ctx, cancel := context.WithCancel(context.Background())
			logger := log.New(zerolog.Nop())
			db := &inmemory.DB{}
			persister := connector.NewPersister(logger, db, time.Second, 3)
			t.Cleanup(func() { stopAndWaitPersister(t, cancel, persister) })
			ps := pipeline.NewService(logger, db)

			pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "classic-default-test"}, pipeline.ProvisionTypeConfig)
			is.NoErr(err)
			ctrl := gomock.NewController(t)
			records := generateRecords(10)

			source, srcDisp := oneRecordSource(ctrl, persister, records)
			dest, destDisp, _ := sdkBatchingDestination(ctrl, persister, nil, 1)
			dest.LastActiveConfig = dest.Config
			dlq, dlqDisp := asserterDestination(ctrl, persister, nil, false)
			pl.DLQ.Plugin = dlq.Plugin

			destRec := &builtinRecorder{Dispenser: destDisp, name: builtinName}
			dlqRec := &builtinRecorder{Dispenser: dlqDisp, name: builtinName}

			_, err = ps.AddConnector(ctx, pl.ID, source.ID)
			is.NoErr(err)
			_, err = ps.AddConnector(ctx, pl.ID, dest.ID)
			is.NoErr(err)

			ls := NewService(logger, testErrRecoveryCfg(),
				testConnectorService{source.ID: source, dest.ID: dest, testDLQID: dlq},
				testProcessorService{},
				testConnectorPluginService{source.Plugin: srcDisp, dest.Plugin: destRec, dlq.Plugin: dlqRec},
				ps, false)

			is.NoErr(ls.Start(ctx, pl.ID))
			waitForRecordsAcked(t, source, records)
			is.NoErr(ls.Stop(ctx, pl.ID, false))
			is.NoErr(ls.WaitPipeline(pl.ID))

			for name, rec := range map[string]*builtinRecorder{"destination": destRec, "dlq": dlqRec} {
				rec.mu.Lock()
				configs := append([]map[string]string(nil), rec.configs...)
				rec.mu.Unlock()
				is.True(len(configs) > 0) // the plugin was configured
				for _, cfg := range configs {
					_, hasSize := cfg["sdk.batch.size"]
					_, hasDelay := cfg["sdk.batch.delay"]
					if hasSize || hasDelay {
						t.Fatalf("%s was configured with batching keys on arch-v2: %v", name, cfg)
					}
				}
			}
		})
	}
}
