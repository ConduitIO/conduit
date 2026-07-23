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
	"strconv"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// BenchmarkSource_Ack measures the Source.Ack hot path in isolation: this is
// the microbenchmark referenced by the sev-0 fix PR
// (fix(engine): persist position before plugin ack - Approach A) as a
// same-sandbox, immediately-reproducible A/B signal, alongside (not instead
// of) the benchi end-to-end reference-pipeline run committed under
// benchi/ack-hot-path/ (see that directory's README for why the full
// benchi/Docker run itself could not be executed in this sandbox).
//
// It uses the real connector.Source / connector.Persister and a real
// in-memory backing store (github.com/conduitio/conduit-commons/database/
// inmemory), with the default production thresholds
// (DefaultPersisterDelayThreshold / DefaultPersisterBundleCountThreshold), so
// the batching/debounce behavior exercised here matches production. A
// background goroutine continuously drains the plugin-side stream for the
// entire benchmark (required in both the pre-fix and post-fix shape: the
// pre-fix code also needs a reader or Source.Ack's synchronous stream.Send
// blocks), so what's actually measured is Source.Ack's own per-call latency
// and the persister's batching overhead - not plugin-stream throughput.
//
// To reproduce the "fails/regresses without the fix" comparison this
// benchmark is used for: run it once on this branch, then run it again after
// temporarily reverting Ack to send synchronously before persister.Persist
// (see the fix PR description's "fails-without-fix" section for the exact,
// reverted diff used), and compare ns/op and allocs/op. The PR description
// reports both numbers.
func BenchmarkSource_Ack(b *testing.B) {
	ctx := context.Background()
	ctrl := gomock.NewController(b)
	logger := log.Nop()
	db := &inmemory.DB{}
	persister := NewPersister(logger, db, DefaultPersisterDelayThreshold, DefaultPersisterBundleCountThreshold)

	is := is.New(b)
	src, sourceMock := newTestSourceWithPersister(ctx, b, ctrl, persister)
	stream := expectSourceOpen(src, sourceMock)
	sourceMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		pconnector.SourceLifecycleOnCreatedRequest{Config: src.Instance.Config.Settings},
	).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	sourceMock.EXPECT().Teardown(gomock.Any(), pconnector.SourceTeardownRequest{}).
		Return(pconnector.SourceTeardownResponse{}, nil).AnyTimes()

	is.NoErr(src.Open(ctx))

	// Continuously drain the plugin-facing side of the stream for the whole
	// benchmark, exactly as a real connector plugin's Recv loop would -
	// otherwise Send (synchronous pre-fix, deferred post-fix) would block
	// once the stream's unbuffered channel has no reader.
	serverStream := stream.Server()
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			if _, err := serverStream.Recv(); err != nil {
				return
			}
		}
	}()

	positions := make([]opencdc.Position, b.N)
	for i := range positions {
		positions[i] = opencdc.Position(strconv.Itoa(i))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := src.Ack(ctx, positions[i:i+1]); err != nil {
			b.Fatalf("Ack: %v", err)
		}
	}
	b.StopTimer()

	is.NoErr(src.Teardown(ctx)) // forces the final flush/drain; closes the stream
	<-drainDone
}
