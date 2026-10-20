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

package connector

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

const deleteTestPassword = "s3cr3t-pa55"

// startedSource creates a source connector that has run before (its
// LastActiveConfig is set), so deleting it runs the plugin's delete hook.
// Its settings carry a password in a URL, which the plugin's error quotes.
func startedSource(t *testing.T, service *Service) *Instance {
	t.Helper()
	is := is.New(t)
	settings := map[string]string{
		"url":       "postgres://repl:" + deleteTestPassword + "@db.internal:5432/orders",
		"slot.name": "conduit_orders",
	}
	conn, err := service.Create(context.Background(), uuid.NewString(), TypeSource, "builtin:postgres",
		uuid.NewString(), Config{Name: "orders", Settings: settings}, ProvisionTypeAPI)
	is.NoErr(err)
	conn.LastActiveConfig = conn.Config
	is.NoErr(service.store.Set(context.Background(), conn.ID, conn))
	return conn
}

// hookFetcher returns a plugin fetcher whose source plugin's delete hook
// returns the next error from errs on each call (nil for success).
func hookFetcher(ctrl *gomock.Controller, conn *Instance, errs ...error) fakePluginFetcher {
	plugin := pmock.NewSourcePlugin(ctrl)
	for _, err := range errs {
		plugin.EXPECT().LifecycleOnDeleted(gomock.Any(), pconnector.SourceLifecycleOnDeletedRequest{Config: conn.LastActiveConfig.Settings}).
			Return(pconnector.SourceLifecycleOnDeletedResponse{}, err)
	}
	plugin.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil).Times(len(errs))
	dispenser := pmock.NewDispenser(ctrl)
	dispenser.EXPECT().DispenseSource().Return(plugin, nil).Times(len(errs))
	return fakePluginFetcher{conn.Plugin: dispenser}
}

// errSlotDrop is what a plugin's delete hook might return: it quotes the
// connection URL, password included.
func errSlotDrop() error {
	return cerrors.New(`failed to drop replication slot "conduit_orders": dial postgres://repl:` +
		deleteTestPassword + `@db.internal:5432/orders: connection refused`)
}

// TestService_Delete_HookFailureKeepsConnector is the #3058 regression test:
// a failing delete hook used to be logged after the record was already gone.
// Now the record stays (in memory and in the store), and the error is coded,
// names the connector and plugin, quotes the plugin's error, and has the
// password scrubbed.
func TestService_Delete_HookFailureKeepsConnector(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	service := NewService(log.Nop(), db, nil)
	conn := startedSource(t, service)

	err := service.Delete(ctx, conn.ID, hookFetcher(gomock.NewController(t), conn, errSlotDrop()))

	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeConnectorDeleteHookFailed)
	is.True(strings.Contains(ce.Message, conn.ID))
	is.True(strings.Contains(ce.Message, "builtin:postgres"))
	is.True(strings.Contains(ce.Message, `replication slot "conduit_orders"`)) // the plugin names the orphan
	is.True(!strings.Contains(ce.Message, deleteTestPassword))                 // never the secret
	is.True(strings.Contains(ce.Suggestion, "--force"))

	got, err := service.Get(ctx, conn.ID)
	is.NoErr(err) // still in memory
	is.Equal(got, conn)

	reloaded := NewService(log.Nop(), db, nil)
	is.NoErr(reloaded.Init(ctx))
	_, err = reloaded.Get(ctx, conn.ID)
	is.NoErr(err) // and still in the store, so provisioning's next boot can retry

	orphans, err := service.ListOrphans(ctx)
	is.NoErr(err)
	is.Equal(len(orphans), 0) // a plain delete never records an orphan
}

// TestService_Delete_RetryAfterHookFailure: once the cause is fixed, the
// same delete succeeds and runs the hook again.
func TestService_Delete_RetryAfterHookFailure(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	service := NewService(log.Nop(), db, nil)
	conn := startedSource(t, service)
	fetcher := hookFetcher(gomock.NewController(t), conn, errSlotDrop(), nil)

	is.True(service.Delete(ctx, conn.ID, fetcher) != nil)
	is.NoErr(service.Delete(ctx, conn.ID, fetcher))

	_, err := service.Get(ctx, conn.ID)
	is.True(cerrors.Is(err, ErrInstanceNotFound))
	reloaded := NewService(log.Nop(), db, nil)
	is.NoErr(reloaded.Init(ctx))
	_, err = reloaded.Get(ctx, conn.ID)
	is.True(cerrors.Is(err, ErrInstanceNotFound))
}

// TestService_Delete_PluginMissing: a plugin that can't be dispensed can't
// run its hook, so it is a hook failure too, not a silent delete.
func TestService_Delete_PluginMissing(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	service := NewService(log.Nop(), &inmemory.DB{}, nil)
	conn := startedSource(t, service)

	err := service.Delete(ctx, conn.ID, fakePluginFetcher{})
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeConnectorDeleteHookFailed)
	_, err = service.Get(ctx, conn.ID)
	is.NoErr(err)
}

// TestService_Delete_NeverStarted: a connector that never ran created
// nothing outside Conduit; its hook is not run and no plugin is dispensed.
func TestService_Delete_NeverStarted(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	service := NewService(log.Nop(), &inmemory.DB{}, nil)
	conn, err := service.Create(ctx, uuid.NewString(), TypeDestination, "builtin:file", uuid.NewString(),
		Config{Name: "out", Settings: map[string]string{"path": "/tmp/out"}}, ProvisionTypeAPI)
	is.NoErr(err)

	// The dispenser has no expectations: dispensing anything fails the test.
	is.NoErr(service.Delete(ctx, conn.ID, fakePluginFetcher{conn.Plugin: pmock.NewDispenser(ctrl)}))
	_, err = service.Get(ctx, conn.ID)
	is.True(cerrors.Is(err, ErrInstanceNotFound))
}

// TestService_ForceDelete_HookFailure: force deletes the record anyway,
// records the orphan in the store, and returns it so the caller can warn.
// Acknowledging clears it; acknowledging twice is a coded not-found.
func TestService_ForceDelete_HookFailure(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	service := NewService(log.Nop(), db, nil)
	conn := startedSource(t, service)

	orphan, err := service.ForceDelete(ctx, conn.ID, hookFetcher(gomock.NewController(t), conn, errSlotDrop()))
	is.NoErr(err)
	is.True(orphan != nil)
	is.Equal(orphan.Version, OrphanVersion)
	is.Equal(orphan.ConnectorID, conn.ID)
	is.Equal(orphan.Plugin, conn.Plugin)
	is.Equal(orphan.PipelineID, conn.PipelineID)
	is.Equal(orphan.Type, "source")
	is.True(strings.Contains(orphan.HookError, "conduit_orders"))
	is.True(!strings.Contains(orphan.HookError, deleteTestPassword))

	w := orphan.Warning()
	is.Equal(w.Code, CodeConnectorOrphanedResources)
	is.True(strings.Contains(w.Message, conn.ID))
	is.True(strings.Contains(w.Suggestion, "--acknowledge-orphan"))

	_, err = service.Get(ctx, conn.ID)
	is.True(cerrors.Is(err, ErrInstanceNotFound))

	// The entry is in the store, readable without a Service (doctor's path).
	stored, err := ListOrphans(ctx, db)
	is.NoErr(err)
	is.Equal(len(stored), 1)
	is.Equal(stored[0].ConnectorID, conn.ID)
	is.Equal(stored[0].HookError, orphan.HookError)

	// Deleted connectors don't come back on the next boot.
	reloaded := NewService(log.Nop(), db, nil)
	is.NoErr(reloaded.Init(ctx))
	is.Equal(len(reloaded.List(ctx)), 0)

	is.NoErr(service.AcknowledgeOrphan(ctx, conn.ID))
	stored, err = ListOrphans(ctx, db)
	is.NoErr(err)
	is.Equal(len(stored), 0)

	err = service.AcknowledgeOrphan(ctx, conn.ID)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeConnectorOrphanNotFound)
}

// TestService_ForceDelete_HookSucceeds: force with a working hook is an
// ordinary delete: no orphan, no entry.
func TestService_ForceDelete_HookSucceeds(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	service := NewService(log.Nop(), db, nil)
	conn := startedSource(t, service)

	orphan, err := service.ForceDelete(ctx, conn.ID, hookFetcher(gomock.NewController(t), conn, nil))
	is.NoErr(err)
	is.True(orphan == nil)
	stored, err := ListOrphans(ctx, db)
	is.NoErr(err)
	is.Equal(len(stored), 0)
	_, err = service.Get(ctx, conn.ID)
	is.True(cerrors.Is(err, ErrInstanceNotFound))
}

// TestDecodeOrphan_RejectsUnknownVersion: a future format must not be read
// as this one.
func TestDecodeOrphan_RejectsUnknownVersion(t *testing.T) {
	is := is.New(t)
	_, err := DecodeOrphan([]byte(`{"version":2,"connectorId":"x"}`))
	is.True(err != nil)
	o, err := DecodeOrphan([]byte(`{"version":1,"connectorId":"x","hookError":"boom"}`))
	is.NoErr(err)
	is.Equal(o.ConnectorID, "x")
}

// TestService_Delete_CodedCauseKeepsDeleteHookCode: errors.Wrap keeps an
// inner coded error's code, so a plugin error that carries its own code used
// to surface as that code instead of connector.delete_hook_failed.
func TestService_Delete_CodedCauseKeepsDeleteHookCode(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	service := NewService(log.Nop(), &inmemory.DB{}, nil)
	conn := startedSource(t, service)

	inner := conduiterr.New(conduiterr.CodeInvalidArgument, "inner problem")
	inner.Suggestion = "inner suggestion"
	inner.ConfigPath = "/some/path"
	fetcher := hookFetcher(gomock.NewController(t), conn, inner)

	err := service.Delete(ctx, conn.ID, fetcher)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeConnectorDeleteHookFailed)
	is.Equal(ce.Suggestion, deleteHookSuggestion)
	is.Equal(ce.ConfigPath, "")
	is.True(cerrors.Is(err, inner)) // the cause stays reachable
}

// TestService_ForceDelete_MissingPluginCode: the plugin is not installed
// (the case the coded-cause bug hit in practice).
func TestService_ForceDelete_MissingPluginIsDeleteHookFailed(t *testing.T) {
	is := is.New(t)
	service := NewService(log.Nop(), &inmemory.DB{}, nil)
	conn := startedSource(t, service)
	err := service.Delete(context.Background(), conn.ID, fakePluginFetcher{})
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeConnectorDeleteHookFailed)
}

// TestService_ForceDelete_SameIDTwiceMergesEntries: force-delete, recreate
// the same ID, force-delete again before acknowledging. The second entry must
// not erase what the first one named.
func TestService_ForceDelete_SameIDTwiceMergesEntries(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	service := NewService(log.Nop(), db, nil)
	conn := startedSource(t, service)

	_, err := service.ForceDelete(ctx, conn.ID, hookFetcher(gomock.NewController(t), conn, cerrors.New("first: slot a_slot busy")))
	is.NoErr(err)

	again, err := service.Create(ctx, conn.ID, TypeSource, conn.Plugin, conn.PipelineID, conn.Config, ProvisionTypeConfig)
	is.NoErr(err)
	again.LastActiveConfig = again.Config
	is.NoErr(service.store.Set(ctx, again.ID, again))

	orphan, err := service.ForceDelete(ctx, again.ID, hookFetcher(gomock.NewController(t), again, cerrors.New("second: publication p_pub busy")))
	is.NoErr(err)
	is.True(strings.Contains(orphan.HookError, "second: publication p_pub busy"))
	is.True(strings.Contains(orphan.HookError, "first: slot a_slot busy"))

	stored, err := ListOrphans(ctx, db)
	is.NoErr(err)
	is.Equal(len(stored), 1)
	is.Equal(stored[0].HookError, orphan.HookError)
}

// diesAfterDB models a process killed mid-sequence: after n successful Sets
// every write fails and nothing more reaches the store.
type diesAfterDB struct {
	*inmemory.DB
	left int
}

func (d *diesAfterDB) Set(ctx context.Context, key string, value []byte) error {
	if d.left <= 0 {
		return cerrors.New("killed")
	}
	d.left--
	return d.DB.Set(ctx, key, value)
}

// TestService_ForceDelete_KilledBetweenTheTwoWrites (invariant 5): without a
// transaction the entry is written first, so a kill between the writes leaves
// an entry next to a record that still exists, never a deleted record with no
// entry. The retry after the restart completes the delete.
func TestService_ForceDelete_KilledBetweenTheTwoWrites(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	inner := &inmemory.DB{}
	setup := NewService(log.Nop(), inner, nil)
	conn := startedSource(t, setup)

	killed := NewService(log.Nop(), &diesAfterDB{DB: inner, left: 1}, nil)
	is.NoErr(killed.Init(ctx))
	_, err := killed.ForceDelete(ctx, conn.ID, hookFetcher(gomock.NewController(t), conn, cerrors.New("hook failed")))
	is.True(err != nil)

	// "Restart" on the surviving store.
	restarted := NewService(log.Nop(), inner, nil)
	is.NoErr(restarted.Init(ctx))
	_, err = restarted.Get(ctx, conn.ID)
	is.NoErr(err) // record survived
	stored, err := ListOrphans(ctx, inner)
	is.NoErr(err)
	is.Equal(len(stored), 1) // entry survived: the leftovers are not forgotten

	// Retry completes; the entry is not duplicated.
	restartedConn, err := restarted.Get(ctx, conn.ID)
	is.NoErr(err)
	orphan, err := restarted.ForceDelete(ctx, conn.ID, hookFetcher(gomock.NewController(t), restartedConn, cerrors.New("hook failed")))
	is.NoErr(err)
	is.True(orphan != nil)
	_, err = restarted.Get(ctx, conn.ID)
	is.True(cerrors.Is(err, ErrInstanceNotFound))
	stored, err = ListOrphans(ctx, inner)
	is.NoErr(err)
	is.Equal(len(stored), 1)
}
