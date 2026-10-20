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
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// TestServiceLifecycle_Start_AfterDeleteHookFailsClosed checks the #3137
// fail-closed rule end to end on the start path: a connector restored after a
// rolled-back delete whose delete hook had run makes Start fail with
// connector.delete_hook_ran, before anything is dispensed or opened, and its
// position is left intact.
func TestServiceLifecycle_Start_AfterDeleteHookFailsClosed(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "p-" + uuid.NewString()}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	conns := connector.NewService(logger, db, nil)
	cfg := connector.Config{Name: "src", Settings: map[string]string{"slot": "s1"}}
	src, err := conns.Create(ctx, uuid.NewString(), connector.TypeSource, "src-plugin", pl.ID, cfg, connector.ProvisionTypeAPI)
	is.NoErr(err)
	src.LastActiveConfig = cfg
	position := connector.SourceState{Position: opencdc.Position("lsn:42")}
	_, err = conns.SetState(ctx, src.ID, position)
	is.NoErr(err)
	pl, err = ps.AddConnector(ctx, pl.ID, src.ID)
	is.NoErr(err)

	// A delete that runs the hook and is then rolled back.
	plugin := pmock.NewSourcePlugin(ctrl)
	plugin.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnDeletedResponse{}, nil)
	plugin.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
	hookDispenser := pmock.NewDispenser(ctrl)
	hookDispenser.EXPECT().DispenseSource().Return(plugin, nil)
	is.NoErr(conns.Delete(ctx, src.ID, testConnectorPluginService{src.Plugin: hookDispenser}))
	is.NoErr(conns.Restore(ctx, src))

	// Starting must not dispense anything: these dispensers have no
	// expectations.
	dlq := dummyDestination(connector.NewPersister(logger, db, 0, 1))
	pl.DLQ.Plugin = dlq.Plugin
	ls := NewService(logger, testErrRecoveryCfg(), testConnectorService{src.ID: src, testDLQID: dlq},
		testProcessorService{}, testConnectorPluginService{
			src.Plugin: pmock.NewDispenser(ctrl),
			dlq.Plugin: pmock.NewDispenser(ctrl),
		}, ps)
	err = ls.Start(ctx, pl.ID)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), connector.CodeConnectorDeleteHookRan.Reason())
	is.Equal(src.State, position)
	is.True(!ls.IsActive(pl.ID))
}
