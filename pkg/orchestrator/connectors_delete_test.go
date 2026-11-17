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

	"github.com/conduitio/conduit-commons/database"
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

// commitFailsDB is a store whose transactions die at commit, as a kill
// during the commit would: nothing the transaction staged is applied.
type commitFailsDB struct {
	*inmemory.DB
}

type commitFailsTxn struct {
	database.Transaction
}

func (t commitFailsTxn) Commit() error {
	t.Transaction.Discard()
	return cerrors.New("killed during commit")
}

func (d commitFailsDB) NewTransaction(ctx context.Context, w bool) (database.Transaction, context.Context, error) {
	txn, ctx, err := d.DB.NewTransaction(ctx, w)
	return commitFailsTxn{txn}, ctx, err
}

// TestConnectorOrchestrator_ForceDelete_KilledAtCommit (invariant 5): the
// orphan entry, the record delete and the pipeline update are one
// transaction. When the process dies at commit none of them is applied, so
// a restart sees the connector exactly as before and the delete can be
// retried.
func TestConnectorOrchestrator_ForceDelete_KilledAtCommit(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	h := newDeleteHarness(t)

	_, _, procsMock, connPluginMock, procPluginMock, lifecycleMock := newMockServices(t)
	expectMutation(t, lifecycleMock, h.pl.ID, true)
	ctrl := gomock.NewController(t)
	plugin := pmock.NewSourcePlugin(ctrl)
	plugin.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnDeletedResponse{}, cerrors.New("hook failed"))
	plugin.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().Return(plugin, nil)
	connPluginMock.EXPECT().NewDispenser(gomock.Any(), h.conn.Plugin, h.conn.ID).Return(dispenser, nil)
	orc := NewOrchestrator(commitFailsDB{h.db}, log.Nop(), h.ps, h.cs, procsMock, connPluginMock, procPluginMock, lifecycleMock)

	_, err := orc.Connectors.ForceDelete(ctx, h.conn.ID)
	is.True(err != nil)

	is.True(h.storedConnectorExists(t))
	orphans, err := connector.ListOrphans(ctx, h.db)
	is.NoErr(err)
	is.Equal(len(orphans), 0)
	fresh := pipeline.NewService(log.Nop(), h.db)
	is.NoErr(fresh.Init(ctx))
	pl, err := fresh.Get(ctx, h.pl.ID)
	is.NoErr(err)
	is.Equal(pl.ConnectorIDs, []string{h.conn.ID})
}

// orchestratorOn is orchestrator with the store the transaction comes from
// chosen by the caller, so a test can make the commit fail.
func (h *deleteHarness) orchestratorOn(t *testing.T, db database.DB, ps PipelineService, hookErr error) *Orchestrator {
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

	return NewOrchestrator(db, log.Nop(), ps, h.cs, procsMock, connPluginMock, procPluginMock, lifecycleMock)
}

// assertDeletableAgain is what a connector has to look like after a delete
// that failed with the hook already run: present in the store, present in
// memory attached to its pipeline, and still carrying LastActiveConfig so
// the next Delete runs the hook instead of taking the never-started
// shortcut. Losing LastActiveConfig here is #3058 reintroduced in the
// recovery path of its own fix.
func (h *deleteHarness) assertDeletableAgain(t *testing.T) {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()

	is.True(h.storedConnectorExists(t))

	conn, err := h.cs.Get(ctx, h.conn.ID)
	is.NoErr(err)
	is.True(conn.LastActiveConfig.Settings != nil)
	is.Equal(conn.LastActiveConfig.Settings["slot.name"], "conduit_orders")

	pl, err := h.ps.Get(ctx, h.pl.ID)
	is.NoErr(err)
	is.Equal(pl.ConnectorIDs, []string{h.conn.ID})
}

// TestConnectorOrchestrator_Delete_HookSucceededThenRemoveConnectorFails:
// the hook ran and released the connector's upstream resource, then a later
// step failed. The transaction is discarded, so nothing reached the store -
// and the rollback has to leave the in-memory state agreeing with it.
//
// The rollback used to call connector.Service.Create, which rebuilds the
// instance from scratch and so dropped LastActiveConfig: the connector came
// back looking like one that had never started, and the next Delete skipped
// its hook. It also re-issued a store write through the transaction that
// was about to be discarded.
func TestConnectorOrchestrator_Delete_HookSucceededThenRemoveConnectorFails(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	h := newDeleteHarness(t)
	orc := h.orchestratorOn(t, h.db, failingRemovePipelines{h.ps}, nil)

	err := orc.Connectors.Delete(ctx, h.conn.ID)
	is.True(err != nil)

	h.assertDeletableAgain(t)
}

// TestConnectorOrchestrator_Delete_HookSucceededThenCommitFails is the same
// for a kill at commit, where the transaction is already discarded by the
// time the rollback runs. Re-issuing the store writes there fails on a dead
// transaction, and rollback.R.MustExecute turns a rollback error into a
// panic - so the rollback has to be in-memory only.
func TestConnectorOrchestrator_Delete_HookSucceededThenCommitFails(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	h := newDeleteHarness(t)
	orc := h.orchestratorOn(t, commitFailsDB{h.db}, h.ps, nil)

	err := orc.Connectors.Delete(ctx, h.conn.ID)
	is.True(err != nil)

	h.assertDeletableAgain(t)
}
