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

package funnel

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/matryer/is"
)

// teardownSource is a fakeSource that, like a real connector.Source, refuses
// acks once torn down (plugin.ErrPluginNotRunning) and remembers every ack that
// was refused.
type teardownSource struct {
	*fakeSource
	mu       sync.Mutex
	down     bool
	refused  []opencdc.Position
	accepted []opencdc.Position
}

func (s *teardownSource) Teardown(ctx context.Context) error {
	s.mu.Lock()
	s.down = true
	s.mu.Unlock()
	return s.fakeSource.Teardown(ctx)
}

func (s *teardownSource) Ack(_ context.Context, p []opencdc.Position) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		s.refused = append(s.refused, p...)
		return plugin.ErrPluginNotRunning
	}
	s.accepted = append(s.accepted, p...)
	return nil
}

func (s *teardownSource) snapshot() (accepted, refused []opencdc.Position) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]opencdc.Position(nil), s.accepted...), append([]opencdc.Position(nil), s.refused...)
}

func stopDrainPositions(recs ...opencdc.Record) []opencdc.Position {
	out := make([]opencdc.Position, len(recs))
	for i, r := range recs {
		out[i] = r.Position
	}
	return out
}

// TestWorker_Stop_DrainsDestinationBeforeSourceTeardown is the regression test
// for #3231. A trailing partial batch is buffered and (pipelined) a full batch
// is still in flight at the destination when Stop arrives. Stop used to tear the
// source down first; the destination was drained later (DestinationTask.Close),
// so every source ack failed with plugin.ErrPluginNotRunning and the written
// records replayed on restart as duplicates. They must be acked while the source
// is still running.
func TestWorker_Stop_DrainsDestinationBeforeSourceTeardown(t *testing.T) {
	for name, pipelined := range map[string]bool{"pipelined": true, "synchronous": false} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.Test(t)

			src := &teardownSource{fakeSource: newFakeSource("src", nil)}
			dest := newFakeDestination("dest")
			dt := NewBatchingDestinationTask("dest", dest, logger, NoOpConnectorMetrics{}, 2, 0)
			if pipelined {
				dt.SetPipelined()
			}
			is.NoErr(dt.Open(ctx))
			destNode := &TaskNode{Task: dt}
			srcNode := &TaskNode{Task: NewSourceTask("src", src, logger, NoOpConnectorMetrics{}), Next: []*TaskNode{destNode}}
			dlq := NewDLQ("dlq", newFakeDestination("dlq"), logger, NoOpConnectorMetrics{}, 10, 10)
			w, err := NewWorker(srcNode, dlq, logger, noop.Timer{})
			is.NoErr(err)
			recs := randomRecords(3)

			var once sync.Once
			unblock := func() {}
			if pipelined {
				blocked, release := dest.blockWrites(recs[0].Position)
				unblock = func() { once.Do(release) }
				defer unblock()                                                           // never leave the flusher wedged on failure
				is.NoErr(w.doTask(ctx, destNode, NewBatch(recs[:2]), newRunAckNacker(w))) // full batch, in flight
				<-blocked
			} else {
				is.NoErr(w.doTask(ctx, destNode, NewBatch(recs[:2]), newRunAckNacker(w))) // flushed inline
			}
			is.NoErr(w.doTask(ctx, destNode, NewBatch(recs[2:3]), newRunAckNacker(w))) // trailing partial, buffered

			stopped := make(chan error, 1)
			go func() { stopped <- w.Stop(ctx) }()

			if pipelined {
				// Stop must wait for the in-flight write instead of tearing the
				// source down underneath it.
				select {
				case <-stopped:
					t.Fatal("Stop returned while a flush was still in flight")
				case <-time.After(50 * time.Millisecond):
				}
				unblock()
			}
			is.NoErr(<-stopped)

			accepted, refused := src.snapshot()
			is.Equal(len(refused), 0) // no ack hit a torn-down source
			is.Equal(accepted, stopDrainPositions(recs...))
			is.Equal(len(dest.receivedPositions()), 3)
			is.NoErr(dt.Close(ctx))
		})
	}
}
