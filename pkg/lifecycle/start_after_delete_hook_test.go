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
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	proc_plugin "github.com/conduitio/conduit/pkg/plugin/processor"
	proc_builtin "github.com/conduitio/conduit/pkg/plugin/processor/builtin"
	"github.com/conduitio/conduit/pkg/processor"
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

// TestServiceLifecycle_Start_AfterDeleteHook_BuildsNothing is the review
// regression test for the refused start (#3137). Nodes are built sources,
// then processors, then destinations, and building a processor marks its
// instance running. When the refusal came from Connector() on a marked
// destination, the pipeline processor built before it stayed "running":
// deleting it failed with ErrProcessorRunning, so the runbook's
// delete-and-recreate remedy failed until a restart. The start must be
// refused before anything is built.
func TestServiceLifecycle_Start_AfterDeleteHook_BuildsNothing(t *testing.T) {
	is := is.New(t)
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()
	logger := log.Nop()
	db := &inmemory.DB{}
	ctrl := gomock.NewController(t)
	persister := connector.NewPersister(logger, db, time.Second, 3)
	defer stopAndWaitPersister(t, killAll, persister)

	ps := pipeline.NewService(logger, db)
	pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "p-" + uuid.NewString()}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)

	// A real processor service: its MakeRunnableProcessor marks the
	// instance running, which is what leaked.
	schemaReg, err := schemaregistry.NewSchemaRegistry(db)
	is.NoErr(err)
	procs := processor.NewService(logger, db,
		proc_plugin.NewPluginService(logger, proc_builtin.NewRegistry(logger, proc_builtin.DefaultBuiltinProcessors, schemaReg), nil))
	newProc := func() *processor.Instance {
		p, err := procs.Create(ctx, uuid.NewString(), "builtin:field.set",
			processor.Parent{ID: pl.ID, Type: processor.ParentTypePipeline},
			processor.Config{Settings: map[string]string{"field": ".Metadata.x", "value": "y"}},
			processor.ProvisionTypeAPI, "")
		is.NoErr(err)
		_, err = ps.AddProcessor(ctx, pl.ID, p.ID)
		is.NoErr(err)
		return p
	}
	proc := newProc()

	source, srcDispenser := asserterSource(ctrl, persister, nil, nil, true, 1)
	_, err = ps.AddConnector(ctx, pl.ID, source.ID)
	is.NoErr(err)

	// The destination is marked: a delete ran its hook and was rolled back.
	conns := connector.NewService(logger, db, persister)
	dcfg := connector.Config{Name: "dst", Settings: map[string]string{"table": "t"}}
	marked, err := conns.Create(ctx, uuid.NewString(), connector.TypeDestination, "dst-plugin", pl.ID, dcfg, connector.ProvisionTypeAPI)
	is.NoErr(err)
	marked.LastActiveConfig = dcfg
	_, err = ps.AddConnector(ctx, pl.ID, marked.ID)
	is.NoErr(err)
	hookDispenser := func() *pmock.Dispenser {
		dst := pmock.NewDestinationPlugin(ctrl)
		dst.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).Return(pconnector.DestinationLifecycleOnDeletedResponse{}, nil)
		dst.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.DestinationTeardownResponse{}, nil)
		d := pmock.NewDispenser(ctrl)
		d.EXPECT().DispenseDestination().Return(dst, nil)
		return d
	}
	is.NoErr(conns.Delete(ctx, marked.ID, testConnectorPluginService{marked.Plugin: hookDispenser()}))
	is.NoErr(conns.Restore(ctx, marked))

	dlq, dlqDispenser := asserterDestination(ctrl, persister, nil, 1)
	pl.DLQ.Plugin = dlq.Plugin
	connMap := testConnectorService{source.ID: source, marked.ID: marked, testDLQID: dlq}
	plugins := testConnectorPluginService{
		source.Plugin: srcDispenser,
		marked.Plugin: pmock.NewDispenser(ctrl), // never asked for a plugin
		dlq.Plugin:    dlqDispenser,
	}
	ls := NewService(logger, testErrRecoveryCfg(), connMap, procs, plugins, ps)

	err = ls.Start(ctx, pl.ID)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), connector.CodeConnectorDeleteHookRan.Reason())

	// Nothing was built: the processor is not running and can be deleted.
	_, err = ps.RemoveProcessor(ctx, pl.ID, proc.ID)
	is.NoErr(err)
	is.NoErr(procs.Delete(ctx, proc.ID))

	// The remedy: delete the marked connector and create a destination again.
	// The pipeline then starts.
	is.NoErr(conns.Delete(ctx, marked.ID, testConnectorPluginService{marked.Plugin: hookDispenser()}))
	_, err = ps.RemoveConnector(ctx, pl.ID, marked.ID)
	is.NoErr(err)
	delete(connMap, marked.ID)
	fresh, freshDispenser := asserterDestination(ctrl, persister, nil, 1)
	connMap[fresh.ID] = fresh
	plugins[fresh.Plugin] = freshDispenser
	_, err = ps.AddConnector(ctx, pl.ID, fresh.ID)
	is.NoErr(err)
	newProc()

	is.NoErr(ls.Start(ctx, pl.ID))
	is.NoErr(ls.Stop(ctx, pl.ID, false))
	is.NoErr(ls.WaitPipeline(pl.ID))
	is.Equal(pipeline.StatusUserStopped, pl.GetStatus())
}
