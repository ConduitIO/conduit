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
	"bytes"
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
	"github.com/conduitio/conduit/pkg/pipeline"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// syncBuffer is a bytes.Buffer safe for the concurrent writes of a logger.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestStart_ArchV2WarnsSizeOnlyDestinationBatching (#3231): with
// sdk.batch.delay the flush timer bounds how long a partial batch waits; with
// sdk.batch.size alone nothing does, as in the connector SDK, and the engine
// says so once at start with a stable code. The pipeline still starts and
// delivers.
func TestStart_ArchV2WarnsSizeOnlyDestinationBatching(t *testing.T) {
	for name, tc := range map[string]struct {
		settings map[string]string
		warn     bool
	}{
		"size only":      {settings: map[string]string{"sdk.batch.size": "5"}, warn: true},
		"size and delay": {settings: map[string]string{"sdk.batch.size": "5", "sdk.batch.delay": "10ms"}},
		"delay only":     {settings: map[string]string{"sdk.batch.delay": "10ms"}},
		"not batched":    {settings: nil},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			h := newBatchingHarness(t, tc.settings, nil, true)
			logs := &syncBuffer{}
			h.ls.logger = log.New(zerolog.New(logs).Level(zerolog.WarnLevel))

			is.NoErr(h.ls.Start(context.Background(), h.pl.ID))
			waitForRecordsAcked(t, h.source, h.records)
			is.NoErr(h.ls.Stop(context.Background(), h.pl.ID, false))
			is.NoErr(h.ls.WaitPipeline(h.pl.ID))

			got := strings.Count(logs.String(), `"code":"`+CodeArchV2DestinationBatchUnboundedWait.Reason()+`"`)
			if tc.warn {
				is.Equal(got, 1)
				is.True(strings.Contains(logs.String(), `"connector_id":"`+h.dest.ID+`"`))
				is.True(strings.Contains(logs.String(), "sdk.batch.delay"))
			} else {
				is.Equal(got, 0)
			}
		})
	}
}

// TestBuildRunnablePipeline_ArchV2TimerFeedersShareRoot (#3231, review of
// #3335): the synchronous flush timer flushes under the mutex of the shared
// boundary node above its destination and poisons that node on failure. It
// learns the node when NewWorker registers each worker as a feeder, so the
// builder must mark the shared roots (funnel.NewSink) before it builds any
// worker. The funnel tests mark the roots themselves, so only a build through
// the service pins the order: built the other way round, every destination
// would have a nil shared node (one source) or the build would fail (two).
func TestBuildRunnablePipeline_ArchV2TimerFeedersShareRoot(t *testing.T) {
	for _, sources := range []int{1, 2} {
		t.Run("sources="+strconv.Itoa(sources), func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			logger := log.New(zerolog.Nop())
			persister := connector.NewPersister(logger, &inmemory.DB{}, time.Second, 3)
			batching := map[string]string{"sdk.batch.size": "100", "sdk.batch.delay": "1s"}

			connSvc := testConnectorService{}
			plugins := testConnectorPluginService{}
			pl := &pipeline.Instance{
				ID:     uuid.NewString(),
				Config: pipeline.Config{Name: "test-pipeline"},
			}
			for range sources {
				src := dummySource(persister)
				connSvc[src.ID] = src
				plugins[src.Plugin] = pmock.NewDispenser(ctrl)
				pl.ConnectorIDs = append(pl.ConnectorIDs, src.ID)
			}
			for range 2 { // two destinations: fan-out, so the timer is synchronous
				dest := dummyDestination(persister)
				dest.Config = connector.Config{Name: "batching-destination", Settings: batching}
				connSvc[dest.ID] = dest
				plugins[dest.Plugin] = pmock.NewDispenser(ctrl)
				pl.ConnectorIDs = append(pl.ConnectorIDs, dest.ID)
			}
			dlq := dummyDestination(persister)
			connSvc[testDLQID] = dlq
			plugins[dlq.Plugin] = pmock.NewDispenser(ctrl)
			pl.DLQ = pipeline.DLQ{Plugin: dlq.Plugin, Settings: map[string]string{}, WindowSize: 3, WindowNackThreshold: 2}
			pl.SetStatus(pipeline.StatusUserStopped)

			ls := NewService(logger, testErrRecoveryCfg(), connSvc, testProcessorService{}, plugins, testPipelineService{}, false)
			got, err := ls.buildRunnablePipeline(ctx, pl)
			is.NoErr(err)
			is.Equal(len(got.workers), sources)

			// No pipeline processors: each destination is its own shared root,
			// attached directly to every worker's tail.
			roots := got.workers[0].FirstTask.Next
			is.Equal(len(roots), 2)
			for _, root := range roots {
				dt, ok := root.Task.(*funnel.DestinationTask)
				is.True(ok)
				feeders, shared := dt.FeederBoundary()
				is.Equal(feeders, got.workers) // every worker, in build order
				is.True(shared == root)        // marked before the workers were built
			}
		})
	}
}
