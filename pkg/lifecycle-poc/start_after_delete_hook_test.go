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
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// TestServiceLifecycle_Start_AfterDeleteHookFailsClosed is the arch-v2 half
// of the #3137 fail-closed rule. A pipeline whose destination was restored
// after a rolled-back delete that ran its delete hook must refuse to start
// with connector.delete_hook_ran before anything is built: no plugin is
// dispensed, the source's processor is not left running (it can be deleted),
// and the destination keeps its position.
func TestServiceLifecycle_Start_AfterDeleteHookFailsClosed(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	persister := connector.NewPersister(logger, db, 0, 1)

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "p-" + uuid.NewString()}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// v2 builds source tasks, including the source's own processors, before
	// destinations, so a processor on the source is the one that a refusal
	// at the destination would leave running.
	source := dummySource(persister)
	_, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)
	procs := newTestProcessorService(t, logger, db)
	proc, err := procs.Create(ctx, uuid.NewString(), "builtin:field.set",
		processor.Parent{ID: source.ID, Type: processor.ParentTypeConnector},
		processor.Config{Settings: map[string]string{"field": ".Metadata.x", "value": "y"}},
		processor.ProvisionTypeAPI, "")
	is.NoErr(err)
	source.ProcessorIDs = []string{proc.ID}

	conns := connector.NewService(logger, db, persister)
	dcfg := connector.Config{Name: "dst", Settings: map[string]string{"table": "t"}}
	marked, err := conns.Create(ctx, uuid.NewString(), connector.TypeDestination, "dst-plugin", pl.ID, dcfg, connector.ProvisionTypeAPI)
	is.NoErr(err)
	marked.LastActiveConfig = dcfg
	position := connector.DestinationState{Positions: map[string]opencdc.Position{source.ID: opencdc.Position("p:42")}}
	_, err = conns.SetState(ctx, marked.ID, position)
	is.NoErr(err)
	_, err = ps.AddConnector(ctx, pl.ID, marked.ID)
	is.NoErr(err)

	dst := pmock.NewDestinationPlugin(ctrl)
	dst.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).Return(pconnector.DestinationLifecycleOnDeletedResponse{}, nil)
	dst.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.DestinationTeardownResponse{}, nil)
	hook := pmock.NewDispenser(ctrl)
	hook.EXPECT().DispenseDestination().Return(dst, nil)
	is.NoErr(conns.Delete(ctx, marked.ID, testConnectorPluginService{marked.Plugin: hook}))
	is.NoErr(conns.Restore(ctx, marked))

	dlq := dummyDestination(persister)
	pl.DLQ.Plugin = dlq.Plugin
	// None of these dispensers has expectations: nothing may be dispensed.
	ls := NewService(logger, testErrRecoveryCfg(),
		testConnectorService{source.ID: source, marked.ID: marked, testDLQID: dlq},
		procs,
		testConnectorPluginService{
			source.Plugin: pmock.NewDispenser(ctrl),
			marked.Plugin: pmock.NewDispenser(ctrl),
			dlq.Plugin:    pmock.NewDispenser(ctrl),
		},
		ps, false)

	err = ls.Start(ctx, pl.ID)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), connector.CodeConnectorDeleteHookRan.Reason())
	is.True(!ls.IsActive(pl.ID))
	is.Equal(marked.State, position)

	is.NoErr(procs.Delete(ctx, proc.ID)) // not left running
}
