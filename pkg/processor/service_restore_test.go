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

package processor

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	proc_mock "github.com/conduitio/conduit/pkg/plugin/processor/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func newRestoreTestProcessor(t *testing.T, s *Service) *Instance {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()
	proc, err := s.Create(
		ctx,
		uuid.NewString(),
		"test-processor",
		Parent{ID: uuid.NewString(), Type: ParentTypePipeline},
		Config{Settings: map[string]string{"field": "val"}, Workers: 2},
		ProvisionTypeAPI,
		"{{ true }}",
	)
	is.NoErr(err)
	return proc
}

func newTestProcessorRegistry(t *testing.T, pluginName string, times int) PluginService {
	t.Helper()
	ctrl := gomock.NewController(t)
	p := proc_mock.NewProcessor(ctrl)
	p.EXPECT().Teardown(gomock.Any()).Return(nil).Times(times)
	reg := newPluginService(t, map[string]sdk.Processor{})
	reg.EXPECT().NewProcessor(gomock.Any(), pluginName, gomock.Any(), gomock.Any()).Return(p, nil).Times(times)
	return reg
}

// TestService_Restore_AfterDelete checks the #3370 contract: the deleted
// instance itself comes back in memory and (outside a transaction) in the
// store, preserving CreatedAt/UpdatedAt without dispensing a new plugin.
func TestService_Restore_AfterDelete(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	db := &inmemory.DB{}
	// Exactly 1 plugin dispense (for Create); Restore must not dispense again.
	reg := newTestProcessorRegistry(t, "test-processor", 1)
	s := NewService(logger, db, reg)
	proc := newRestoreTestProcessor(t, s)

	is.NoErr(s.Delete(ctx, proc.ID))
	is.NoErr(s.Restore(ctx, proc))

	got, err := s.Get(ctx, proc.ID)
	is.NoErr(err)
	is.True(got == proc)

	// Inspector remains usable on the restored instance.
	sess := got.InspectIn(ctx, "test-session")
	is.True(sess != nil)
	got.Close()

	// Outside a transaction the delete reached the store, so Restore wrote the
	// record back: a restart reloads it with its original timestamps and config.
	restarted := NewService(logger, db, newPluginService(t, map[string]sdk.Processor{}))
	is.NoErr(restarted.Init(ctx))
	stored, err := restarted.Get(ctx, proc.ID)
	is.NoErr(err)
	is.Equal(stored.Config, proc.Config)
	is.Equal(stored.Condition, proc.Condition)
	is.True(stored.CreatedAt.Equal(proc.CreatedAt))
	is.True(stored.UpdatedAt.Equal(proc.UpdatedAt))
}

func TestService_Restore_SameInstanceIsNoOp(t *testing.T) {
	is := is.New(t)
	reg := newTestProcessorRegistry(t, "test-processor", 1)
	s := NewService(log.Nop(), &inmemory.DB{}, reg)
	proc := newRestoreTestProcessor(t, s)

	is.NoErr(s.Restore(context.Background(), proc))
	got, err := s.Get(context.Background(), proc.ID)
	is.NoErr(err)
	is.True(got == proc)
}

// TestService_Restore_RefusesToReplace ensures a rollback cannot overwrite a
// different live instance registered under the same ID, and rejects nil.
func TestService_Restore_RefusesToReplace(t *testing.T) {
	is := is.New(t)
	reg := newTestProcessorRegistry(t, "test-processor", 1)
	s := NewService(log.Nop(), &inmemory.DB{}, reg)
	proc := newRestoreTestProcessor(t, s)

	sOther := NewService(log.Nop(), &inmemory.DB{}, newTestProcessorRegistry(t, "test-processor", 1))
	other, err := sOther.Create(
		context.Background(),
		proc.ID,
		proc.Plugin,
		proc.Parent,
		proc.Config,
		ProvisionTypeAPI,
		proc.Condition,
	)
	is.NoErr(err)
	is.True(s.Restore(context.Background(), other) != nil)
	got, err := s.Get(context.Background(), proc.ID)
	is.NoErr(err)
	is.True(got == proc)

	is.True(s.Restore(context.Background(), nil) != nil)
}

// TestService_Restore_InTransactionDoesNotWrite verifies that Restore skips the
// store write when ctx carries a transaction: the caller reverts the store via
// txn.Discard, and writing after a failed Commit hits ErrDiscardedTxn.
func TestService_Restore_InTransactionDoesNotWrite(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	reg := newTestProcessorRegistry(t, "test-processor", 1)
	s := NewService(log.Nop(), db, reg)
	proc := newRestoreTestProcessor(t, s)

	txn, tctx, err := db.NewTransaction(ctx, true)
	is.NoErr(err)
	is.NoErr(s.Delete(tctx, proc.ID))
	is.NoErr(s.Restore(tctx, proc))
	// Restore staged no write in txn, so committing txn applies the delete alone.
	is.NoErr(txn.Commit())
	all, err := NewStore(db).GetAll(ctx)
	is.NoErr(err)
	_, stored := all[proc.ID]
	is.True(!stored)

	got, err := s.Get(ctx, proc.ID)
	is.NoErr(err)
	is.True(got == proc) // memory restored regardless
}
