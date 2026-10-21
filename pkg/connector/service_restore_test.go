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
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func newRestoreTestConnector(t *testing.T, s *Service) *Instance {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()
	conn, err := s.Create(ctx, uuid.NewString(), TypeSource, "test-plugin", uuid.NewString(),
		Config{Name: "src", Settings: map[string]string{"table": "orders"}}, ProvisionTypeAPI)
	is.NoErr(err)
	_, err = s.SetState(ctx, conn.ID, SourceState{Position: opencdc.Position("orders:42")})
	is.NoErr(err)
	return conn
}

// TestService_Restore_AfterDelete checks the #3137 contract: the deleted
// instance itself comes back, in memory and in the store, with its state.
func TestService_Restore_AfterDelete(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	ctrl := gomock.NewController(t)
	db := &inmemory.DB{}
	s := NewService(logger, db, nil)
	conn := newRestoreTestConnector(t, s)

	is.NoErr(s.Delete(ctx, conn.ID, fakePluginFetcher{conn.Plugin: pmock.NewDispenser(ctrl)}))
	is.NoErr(s.Restore(ctx, conn))

	got, err := s.Get(ctx, conn.ID)
	is.NoErr(err)
	is.True(got == conn)
	is.Equal(got.State, SourceState{Position: opencdc.Position("orders:42")})

	// The delete really reached the store (no transaction), so Restore wrote
	// the record back: a restart sees the connector with its position.
	restarted := NewService(logger, db, nil)
	is.NoErr(restarted.Init(ctx))
	stored, err := restarted.Get(ctx, conn.ID)
	is.NoErr(err)
	is.Equal(stored.State, conn.State)
	is.True(stored.CreatedAt.Equal(conn.CreatedAt))
}

func TestService_Restore_SameInstanceIsNoOp(t *testing.T) {
	is := is.New(t)
	s := NewService(log.Nop(), &inmemory.DB{}, nil)
	conn := newRestoreTestConnector(t, s)

	is.NoErr(s.Restore(context.Background(), conn))
	got, err := s.Get(context.Background(), conn.ID)
	is.NoErr(err)
	is.True(got == conn)
}

// TestService_Restore_RefusesToReplace makes sure a rollback can never swap
// out a different live instance registered under the same ID.
func TestService_Restore_RefusesToReplace(t *testing.T) {
	is := is.New(t)
	s := NewService(log.Nop(), &inmemory.DB{}, nil)
	conn := newRestoreTestConnector(t, s)

	other := &Instance{ID: conn.ID, Type: TypeSource}
	is.True(s.Restore(context.Background(), other) != nil)
	got, err := s.Get(context.Background(), conn.ID)
	is.NoErr(err)
	is.True(got == conn)

	is.True(s.Restore(context.Background(), nil) != nil)
}

// TestService_Restore_AfterDeleteHook covers the fail-closed rule (#3137,
// invariant 3). Once Delete delivered the "deleted" event to the plugin, the
// plugin may have released what the position points into (a Postgres slot).
// Restore keeps the position untouched, and the restored connector refuses
// to start with CodeConnectorDeleteHookRan. Both the source and the
// destination branch of the hook are covered.
func TestService_Restore_AfterDeleteHook(t *testing.T) {
	testCases := []struct {
		name     string
		started  bool  // has a LastActiveConfig, so the hook delivers "deleted"
		hookErr  error // what the plugin's LifecycleOnDeleted returns
		wantMark bool
	}{
		{name: "hook succeeded", started: true, hookErr: nil, wantMark: true},
		// The plugin got the event; it may have released part of it.
		{name: "hook failed", started: true, hookErr: cerrors.New("could not drop slot"), wantMark: true},
		// An older plugin without lifecycle events did nothing.
		{name: "hook not implemented", started: true, hookErr: pconnector.ErrUnimplemented, wantMark: false},
		// Never started: there is no "deleted" event to deliver.
		{name: "hook did not run", started: false, wantMark: false},
	}
	for _, typ := range []Type{TypeSource, TypeDestination} {
		for _, tc := range testCases {
			t.Run(typ.String()+"/"+tc.name, func(t *testing.T) {
				is := is.New(t)
				ctx := context.Background()
				ctrl := gomock.NewController(t)
				s := NewService(log.Nop(), &inmemory.DB{}, nil)

				cfg := Config{Name: "conn", Settings: map[string]string{"table": "orders"}}
				conn, err := s.Create(ctx, uuid.NewString(), typ, "test-plugin", uuid.NewString(), cfg, ProvisionTypeAPI)
				is.NoErr(err)
				var state any = SourceState{Position: opencdc.Position("orders:42")}
				if typ == TypeDestination {
					state = DestinationState{Positions: map[string]opencdc.Position{"src": opencdc.Position("orders:42")}}
				}
				if tc.started {
					conn.LastActiveConfig = cfg
				}
				_, err = s.SetState(ctx, conn.ID, state)
				is.NoErr(err)
				wantLAC := conn.LastActiveConfig

				dispenser := hookDispenser(ctrl, typ, tc.started, tc.hookErr)
				plugins := fakePluginFetcher{conn.Plugin: dispenser}

				is.NoErr(s.Delete(ctx, conn.ID, plugins))
				is.NoErr(s.Restore(ctx, conn))

				got, err := s.Get(ctx, conn.ID)
				is.NoErr(err)
				is.True(got == conn)
				is.Equal(got.State, state) // never cleared
				is.Equal(got.LastActiveConfig, wantLAC)

				// The next start builds the connector first.
				c, err := got.Connector(ctx, plugins)
				if tc.wantMark {
					is.True(c == nil)
					ce, ok := conduiterr.Get(err)
					is.True(ok)
					is.Equal(ce.Code.Reason(), CodeConnectorDeleteHookRan.Reason())
					is.True(ce.Suggestion != "")
					is.True(strings.Contains(err.Error(), conn.ID))
					is.Equal(got.State, state) // the refusal leaves the position intact
				} else {
					is.NoErr(err)
					is.True(c != nil)
				}

				// A marked connector can still be deleted (the remedy).
				if tc.wantMark {
					is.NoErr(s.Delete(ctx, conn.ID, fakePluginFetcher{conn.Plugin: hookDispenser(ctrl, typ, true, nil)}))
				}
			})
		}
	}
}

// hookDispenser returns a dispenser whose plugin answers one delete hook
// with hookErr, or a dispenser that is never asked for a plugin when the
// connector has not started (no "deleted" event to deliver).
func hookDispenser(ctrl *gomock.Controller, typ Type, started bool, hookErr error) *pmock.Dispenser {
	dispenser := pmock.NewDispenser(ctrl)
	if !started {
		return dispenser
	}
	if typ == TypeSource {
		src := pmock.NewSourcePlugin(ctrl)
		src.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnDeletedResponse{}, hookErr)
		src.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
		dispenser.EXPECT().DispenseSource().Return(src, nil)
		return dispenser
	}
	dst := pmock.NewDestinationPlugin(ctrl)
	dst.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).Return(pconnector.DestinationLifecycleOnDeletedResponse{}, hookErr)
	dst.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.DestinationTeardownResponse{}, nil)
	dispenser.EXPECT().DispenseDestination().Return(dst, nil)
	return dispenser
}

// TestService_Restore_InTransactionDoesNotWrite checks that Restore leaves
// the store alone inside a transaction: the caller discards it, and a
// backend may reject statements in an aborted transaction.
func TestService_Restore_InTransactionDoesNotWrite(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	db := &inmemory.DB{}
	s := NewService(log.Nop(), db, nil)
	conn := newRestoreTestConnector(t, s)

	txn, tctx, err := db.NewTransaction(ctx, true)
	is.NoErr(err)
	is.NoErr(s.Delete(tctx, conn.ID, fakePluginFetcher{conn.Plugin: pmock.NewDispenser(ctrl)}))
	is.NoErr(s.Restore(tctx, conn))
	// Nothing was written in txn after the delete: committing it now would
	// apply the delete alone.
	is.NoErr(txn.Commit())
	all, err := NewStore(db, log.Nop()).GetAll(ctx)
	is.NoErr(err)
	_, stored := all[conn.ID]
	is.True(!stored)

	got, err := s.Get(ctx, conn.ID)
	is.NoErr(err)
	is.True(got == conn) // memory restored regardless
}

func TestService_Restore_RefusesDLQ(t *testing.T) {
	is := is.New(t)
	s := NewService(log.Nop(), &inmemory.DB{}, nil)
	is.True(s.Restore(context.Background(), &Instance{ID: "dlq", ProvisionedBy: ProvisionTypeDLQ}) != nil)
	_, err := s.Get(context.Background(), "dlq")
	is.True(err != nil)
}
