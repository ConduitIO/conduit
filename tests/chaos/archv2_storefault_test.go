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

package chaos

import (
	"context"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
	"github.com/matryer/is"
)

// TestArchV2_StoreFault_FailsRunWithoutAck is the arch-v2 counterpart of
// TestSIGKILL_StoreFault_PruningUpstream_NoGap, for #2929. A real
// *connector.Source over badger with the store fault armed after the first
// connector write runs in a real funnel.Worker against a pruning upstream.
// The producer is capped at holdAt, so the source ends up idle in Read.
//
// WatchConnectorErrors must hand the persist failure to the run within
// bounded time; canceling the context the worker was opened with (what
// killing the pipeline's tomb does in pkg/lifecycle-poc) must end Do even
// though the source is blocked in Read. Throughout, the upstream must not
// commit past the position badger actually holds (invariant 1).
//
// Before #2929 nothing read the source's Errors() under arch-v2: the run kept
// reading and the failure surfaced only at Teardown.
func TestArchV2_StoreFault_FailsRunWithoutAck(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	cfg := childEnv{
		dbDir:          dir + "/db",
		upstreamDir:    dir + "/upstream",
		prune:          true,
		paceMS:         2,
		total:          60,
		holdAt:         30,
		persistDelayMS: 20,
		storeFailAfter: 1,
	}

	built, err := buildChild(context.Background(), cfg)
	is.NoErr(err)

	logger := log.Test(t)
	srcTask := funnel.NewSourceTask("chaos-storefault-source", built.src, logger, funnel.NoOpConnectorMetrics{})
	dest := &synthDestination{id: "chaos-storefault-dest"}
	destTask := funnel.NewDestinationTask("chaos-storefault-dest", dest, logger, funnel.NoOpConnectorMetrics{})
	srcNode := &funnel.TaskNode{Task: srcTask, Next: []*funnel.TaskNode{{Task: destTask}}}
	dlq := funnel.NewDLQ("chaos-storefault-dlq", &synthDestination{id: "chaos-storefault-dlq"}, logger, funnel.NoOpConnectorMetrics{}, 0, 0)

	worker, err := funnel.NewWorker(srcNode, dlq, logger, noop.Timer{})
	is.NoErr(err)

	// The run's context, as the tomb's is in pkg/lifecycle-poc: the plugin
	// stream derives from it, so canceling it unblocks Read.
	runCtx, kill := context.WithCancel(context.Background())
	defer kill()
	is.NoErr(worker.Open(runCtx))

	failed := make(chan error, 1)
	stopWatch := worker.WatchConnectorErrors(runCtx, func(err error) {
		failed <- err
		kill()
	})
	doErr := make(chan error, 1)
	go func() { doErr <- worker.Do(runCtx) }()

	select {
	case err := <-failed:
		is.True(cerrors.Is(err, errInjectedStoreFault)) // the persist failure itself, not a side effect
	case <-time.After(30 * time.Second):
		t.Fatalf("#2929 regression: the store fault never reached the arch-v2 run (delivered %d records)",
			dest.deliveredCount())
	}

	select {
	case <-doErr: // context.Canceled, or the read error the cancellation caused
	case <-time.After(30 * time.Second):
		t.Fatal("Do did not return after the run was killed; a blocked Read was not interrupted")
	}
	stopWatch()
	// Teardown's final flush fails too and Teardown returns that error; the
	// failed positions stay unacknowledged either way.
	_ = worker.Close(context.Background())

	committed, err := built.upstream.Committed()
	is.NoErr(err)
	persisted := drainPersistedPosition(t, built)
	if committed > persisted {
		t.Fatalf("invariant 1 violated: upstream committed through %d but the store holds %d", committed, persisted)
	}
	if persisted >= cfg.holdAt {
		t.Fatalf("store fault not exercised: badger holds %d, the producer stopped at %d", persisted, cfg.holdAt)
	}
}
