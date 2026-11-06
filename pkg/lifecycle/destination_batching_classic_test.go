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
	"maps"
	"sync"
	"testing"
	"time"

	commonsconfig "github.com/conduitio/conduit-commons/config"
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

// configureCapturingDispenser wraps a test dispenser: it attaches a plugin
// specification, as the plugin registries do, and records the settings each
// dispensed destination's Configure receives.
type configureCapturingDispenser struct {
	connectorPlugin.Dispenser
	spec pconnector.Specification

	mu  sync.Mutex
	got map[string]string
}

func (d *configureCapturingDispenser) Specification() (pconnector.Specification, bool) {
	return d.spec, true
}

func (d *configureCapturingDispenser) DispenseDestination() (connectorPlugin.DestinationPlugin, error) {
	p, err := d.Dispenser.DispenseDestination()
	if err != nil {
		return nil, err
	}
	return configureCapturingPlugin{DestinationPlugin: p, d: d}, nil
}

func (d *configureCapturingDispenser) configured() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return maps.Clone(d.got)
}

type configureCapturingPlugin struct {
	connectorPlugin.DestinationPlugin
	d *configureCapturingDispenser
}

func (p configureCapturingPlugin) Configure(ctx context.Context, req pconnector.DestinationConfigureRequest) (pconnector.DestinationConfigureResponse, error) {
	p.d.mu.Lock()
	p.d.got = maps.Clone(req.Config)
	p.d.mu.Unlock()
	return p.DestinationPlugin.Configure(ctx, req)
}

// TestServiceLifecycle_DestinationSettingsVerbatim pins constraint 7 of
// docs/design-documents/20261103-archv2-destination-batch-timing.md: the
// classic engine does not change. Its destination plugin receives the
// settings verbatim, with no "0" injected for sdk.batch.*, even when the
// connector's spec batches by default (S3: 1000 / 1 s), because classic reads
// destination acks on their own goroutine and connectors that batch by default
// depend on the SDK doing it. Passes on main; fails if the arch-v2 override
// ever reaches this engine.
func TestServiceLifecycle_DestinationSettingsVerbatim(t *testing.T) {
	for name, settings := range map[string]map[string]string{
		"nothing set (spec default batches)": {"foo": "bar"},
		"pipeline sets sdk.batch.*":          {"sdk.batch.size": "100", "sdk.batch.delay": "1s"},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx, killAll := context.WithCancel(context.Background())
			defer killAll()
			logger := log.New(zerolog.Nop())
			db := &inmemory.DB{}
			persister := connector.NewPersister(logger, db, time.Second, 3)
			defer stopAndWaitPersister(t, killAll, persister)

			ps := pipeline.NewService(logger, db)
			pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
			is.NoErr(err)

			ctrl := gomock.NewController(t)
			wantRecords := generateRecords(10)
			source, srcDispenser := asserterSource(ctrl, persister, wantRecords, nil, true, 1)
			destination, destDispenser := asserterDestination(ctrl, persister, wantRecords, 1)
			destination.Config.Settings = settings
			destination.LastActiveConfig = destination.Config
			dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, 1)
			pl.DLQ.Plugin = dlq.Plugin

			_, err = ps.AddConnector(ctx, pl.ID, source.ID)
			is.NoErr(err)
			_, err = ps.AddConnector(ctx, pl.ID, destination.ID)
			is.NoErr(err)

			capturing := &configureCapturingDispenser{
				Dispenser: destDispenser,
				spec: pconnector.Specification{DestinationParams: commonsconfig.Parameters{
					"sdk.batch.size":  {Default: "1000"},
					"sdk.batch.delay": {Default: "1s"},
				}},
			}
			ls := NewService(logger, testErrRecoveryCfg(), testConnectorService{
				source.ID:      source,
				destination.ID: destination,
				testDLQID:      dlq,
			},
				testProcessorService{},
				testConnectorPluginService{
					source.Plugin:      srcDispenser,
					destination.Plugin: capturing,
					dlq.Plugin:         dlqDispenser,
				}, ps)

			is.NoErr(ls.Start(ctx, pl.ID))
			waitForSourceAcked(t, source, wantRecords)
			is.Equal(capturing.configured(), settings)

			is.NoErr(ls.Stop(ctx, pl.ID, false))
			is.NoErr(ls.WaitPipeline(pl.ID))
		})
	}
}
