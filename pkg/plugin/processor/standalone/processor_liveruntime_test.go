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

package standalone

import (
	"context"
	"testing"
	"time"

	"github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	"github.com/matryer/is"
	"github.com/stealthrocket/wazergo"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// newLiveProcessor builds a wasmProcessor on a runtime configured like the LIVE
// record-path one (newRuntime(ctx, false): no close-on-context-done), whereas the
// suite's shared TestRuntime has it on.
func newLiveProcessor(t *testing.T, binary []byte) *wasmProcessor {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()

	rt := newRuntime(ctx, false)
	t.Cleanup(func() { _ = rt.Close(ctx) })
	_, err := wasi_snapshot_preview1.Instantiate(ctx, rt)
	is.NoErr(err)
	host, err := wazergo.Compile(ctx, rt, hostModule)
	is.NoErr(err)
	var mod wazero.CompiledModule
	mod, err = rt.CompileModule(ctx, binary)
	is.NoErr(err)

	// log.Nop: the guest outlives the test body and logs when it stops.
	p, err := newWASMProcessor(ctx, rt, mod, host, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", log.Nop())
	is.NoErr(err)
	return p
}

// TestWASMProcessor_LiveRuntime_AbandonedCommand_TeardownReturns is the abandon
// regression test on the live-runtime configuration. It never kills from outside
// (the stop timeout is an hour), so it is race-detector safe and runs in the
// normal -race CI job: the guest's late reply is dropped, it returns to
// command_request, sees the shutdown signal and exits on its own.
func TestWASMProcessor_LiveRuntime_AbandonedCommand_TeardownReturns(t *testing.T) {
	is := is.New(t)
	setStopTimeouts(t, time.Hour, time.Hour)

	underTest := newLiveProcessor(t, StallBinary)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnceGuestAccepts(underTest, cancel)

	_, err := underTest.executeCommand(ctx, specifyRequest())
	is.True(cerrors.Is(err, ErrCommandTimeout))

	err = teardownWithin(t, underTest, 2*time.Minute)
	is.True(cerrors.Is(err, ErrCommandTimeout)) // the skipped teardown command is reported
}
