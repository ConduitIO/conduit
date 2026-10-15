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
