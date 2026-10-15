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

package orchestrator

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// failingRemovePipelines is a real pipeline service whose RemoveConnector
// fails, to show what a failure after the connector delete does to the
// transaction.
type failingRemovePipelines struct {
	*pipeline.Service
}

func (failingRemovePipelines) RemoveConnector(context.Context, string, string) (*pipeline.Instance, error) {
	return nil, cerrors.New("remove connector failed")
}

// deleteHarness wires real pipeline and connector services over one
// in-memory store, so the store transaction ConnectorOrchestrator.Delete
// opens is the one the writes go through.
type deleteHarness struct {
	db   *inmemory.DB
	ps   *pipeline.Service
	cs   *connector.Service
	pl   *pipeline.Instance
	conn *connector.Instance
}

func newDeleteHarness(t *testing.T) *deleteHarness {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()
	h := &deleteHarness{db: &inmemory.DB{}}
	h.ps = pipeline.NewService(log.Nop(), h.db)
	h.cs = connector.NewService(log.Nop(), h.db, nil)

	var err error
	h.pl, err = h.ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "orders"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)
	h.conn, err = h.cs.Create(ctx, uuid.NewString(), connector.TypeSource, "builtin:postgres", h.pl.ID,
		connector.Config{Name: "orders-src", Settings: map[string]string{"slot.name": "conduit_orders"}}, connector.ProvisionTypeAPI)
	is.NoErr(err)
	h.conn.LastActiveConfig = h.conn.Config // it has run, so the hook runs
	_, err = h.ps.AddConnector(ctx, h.pl.ID, h.conn.ID)
	is.NoErr(err)
	return h
}

// orchestrator returns a ConnectorOrchestrator whose plugin's delete hook
// returns hookErr.
func (h *deleteHarness) orchestrator(t *testing.T, ps PipelineService, hookErr error) *Orchestrator {
	t.Helper()
	_, _, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)
	expectMutation(t, lifecycleMock, h.pl.ID, true)

	ctrl := gomock.NewController(t)
	plugin := pmock.NewSourcePlugin(ctrl)
	plugin.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnDeletedResponse{}, hookErr)
	plugin.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().Return(plugin, nil)
	connPluginMock.EXPECT().NewDispenser(gomock.Any(), h.conn.Plugin, h.conn.ID).Return(dispenser, nil)

	return NewOrchestrator(h.db, log.Nop(), ps, h.cs, procsMock, connPluginMock, procPluginMock, lifecycleMock)
}

// storedConnectorExists reloads the connector store, as a restart would.
func (h *deleteHarness) storedConnectorExists(t *testing.T) bool {
	t.Helper()
	is := is.New(t)
	fresh := connector.NewService(log.Nop(), h.db, nil)
	is.NoErr(fresh.Init(context.Background()))
	_, err := fresh.Get(context.Background(), h.conn.ID)
	return err == nil
}

// TestConnectorOrchestrator_Delete_HookFailureKeepsEverything: the API path
// of #3058. The connector stays, still attached to its pipeline, and the
// coded error reaches the caller.
func TestConnectorOrchestrator_Delete_HookFailureKeepsEverything(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	h := newDeleteHarness(t)
	orc := h.orchestrator(t, h.ps, cerrors.New(`could not drop slot "conduit_orders"`))

	err := orc.Connectors.Delete(ctx, h.conn.ID)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, connector.CodeConnectorDeleteHookFailed)

	is.True(h.storedConnectorExists(t))
	pl, err := h.ps.Get(ctx, h.pl.ID)
	is.NoErr(err)
	is.Equal(pl.ConnectorIDs, []string{h.conn.ID})
}

// TestConnectorOrchestrator_ForceDelete_Commits: a forced delete with a
// failing hook removes the connector from the store and the pipeline and
// commits the orphan entry with it.
func TestConnectorOrchestrator_ForceDelete_Commits(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	h := newDeleteHarness(t)
	orc := h.orchestrator(t, h.ps, cerrors.New(`could not drop slot "conduit_orders"`))

	orphan, err := orc.Connectors.ForceDelete(ctx, h.conn.ID)
	is.NoErr(err)
	is.True(orphan != nil)
	is.Equal(orphan.ConnectorID, h.conn.ID)

	is.True(!h.storedConnectorExists(t))
	pl, err := h.ps.Get(ctx, h.pl.ID)
	is.NoErr(err)
	is.Equal(len(pl.ConnectorIDs), 0)
	orphans, err := connector.ListOrphans(ctx, h.db)
	is.NoErr(err)
	is.Equal(len(orphans), 1)

	// Acknowledging is refused while a connector with the ID exists, and
	// clears the entry once it does not.
	is.NoErr(orc.Connectors.AcknowledgeOrphan(ctx, h.conn.ID))
	orphans, err = connector.ListOrphans(ctx, h.db)
	is.NoErr(err)
	is.Equal(len(orphans), 0)
}

// TestConnectorOrchestrator_ForceDelete_AtomicOnLaterFailure is invariant 5
// for the forced delete: when a step after the connector delete fails, the
// transaction is discarded, and neither the record deletion nor the orphan
// entry reaches the store.
func TestConnectorOrchestrator_ForceDelete_AtomicOnLaterFailure(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	h := newDeleteHarness(t)
	orc := h.orchestrator(t, failingRemovePipelines{h.ps}, cerrors.New("hook failed"))

	_, err := orc.Connectors.ForceDelete(ctx, h.conn.ID)
	is.True(err != nil)

	is.True(h.storedConnectorExists(t))
	orphans, err := connector.ListOrphans(ctx, h.db)
	is.NoErr(err)
	is.Equal(len(orphans), 0)
}

func TestConnectorOrchestrator_AcknowledgeOrphan_RefusedForExistingConnector(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	h := newDeleteHarness(t)
	_, _, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)
	orc := NewOrchestrator(h.db, log.Nop(), h.ps, h.cs, procsMock, connPluginMock, procPluginMock, lifecycleMock)

	err := orc.Connectors.AcknowledgeOrphan(ctx, h.conn.ID)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)

	err = orc.Connectors.AcknowledgeOrphan(ctx, uuid.NewString())
	ce, ok = conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, connector.CodeConnectorOrphanNotFound)
}
