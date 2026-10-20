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

// recordingBuiltin is a dispenser that looks like the built-in connector name
// and records the settings its destination plugins are configured with.
type recordingBuiltin struct {
	connectorPlugin.Dispenser
	name string

	mu      sync.Mutex
	configs []map[string]string
}

func (r *recordingBuiltin) BuiltinPluginName() (string, bool) { return r.name, true }

func (r *recordingBuiltin) DispenseDestination() (connectorPlugin.DestinationPlugin, error) {
	p, err := r.Dispenser.DispenseDestination()
	if err != nil {
		return nil, err
	}
	return &recordingDestination{DestinationPlugin: p, r: r}, nil
}

func (r *recordingBuiltin) configured() []map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]string(nil), r.configs...)
}

type recordingDestination struct {
	connectorPlugin.DestinationPlugin
	r *recordingBuiltin
}

func (d *recordingDestination) Configure(ctx context.Context, req pconnector.DestinationConfigureRequest) (pconnector.DestinationConfigureResponse, error) {
	cp := map[string]string{}
	for k, v := range req.Config {
		cp[k] = v
	}
	d.r.mu.Lock()
	d.r.configs = append(d.r.configs, cp)
	d.r.mu.Unlock()
	return d.DestinationPlugin.Configure(ctx, req)
}

// TestServiceLifecycle_ClassicDefaultDestinationBatching: on the classic
// engine a built-in postgres destination with no batching settings is
// configured with the default sdk.batch.size/sdk.batch.delay, and the DLQ
// destination, a regular destination object built by buildDLQHandlerNode, is
// not.
func TestServiceLifecycle_ClassicDefaultDestinationBatching(t *testing.T) {
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
	destination.LastActiveConfig = destination.Config // no lifecycle event
	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, 1)
	pl.DLQ.Plugin = dlq.Plugin

	destRec := &recordingBuiltin{Dispenser: destDispenser, name: "postgres"}
	dlqRec := &recordingBuiltin{Dispenser: dlqDispenser, name: "postgres"}

	_, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	_, err = ps.AddConnector(ctx, pl.ID, destination.ID)
	is.NoErr(err)

	ls := NewService(logger, testErrRecoveryCfg(), testConnectorService{
		source.ID:      source,
		destination.ID: destination,
		testDLQID:      dlq,
	},
		testProcessorService{},
		testConnectorPluginService{
			source.Plugin:      srcDispenser,
			destination.Plugin: destRec,
			dlq.Plugin:         dlqRec,
		}, ps)

	is.NoErr(ls.Start(ctx, pl.ID))
	waitForSourceAcked(t, source, wantRecords)
	is.NoErr(ls.Stop(ctx, pl.ID, false))
	is.NoErr(ls.WaitPipeline(pl.ID))

	got := destRec.configured()
	is.Equal(len(got), 1)
	is.Equal(got[0]["sdk.batch.size"], "1000")
	is.Equal(got[0]["sdk.batch.delay"], "100ms")
	// The stored connector config is not modified.
	is.Equal(len(destination.Config.Settings), 0)

	gotDLQ := dlqRec.configured()
	is.Equal(len(gotDLQ), 1)
	_, hasSize := gotDLQ[0]["sdk.batch.size"]
	_, hasDelay := gotDLQ[0]["sdk.batch.delay"]
	is.True(!hasSize && !hasDelay) // the DLQ never gets the default
}
