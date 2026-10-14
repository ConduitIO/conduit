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

package engines

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
)

// V2 drives pkg/lifecycle-poc (arch-v2).
type V2 struct{}

// Name implements Engine.
func (V2) Name() string { return "v2" }

// Start implements Engine. It builds what lifecycle-poc.Service builds: one
// funnel.Worker per source, each with its own DLQ, all converging on a shared
// funnel.Sink that owns the destinations. Open order, error watching and
// shutdown order follow Service.runPipeline: sink opened before any worker,
// closed only after every worker has exited.
func (V2) Start(ctx context.Context, t Topology) (Run, error) {
	if len(t.Sources) == 0 || len(t.Destinations) == 0 {
		return nil, cerrors.New("engines: need at least one source and one destination")
	}
	logger := log.Nop()

	roots := make([]*funnel.TaskNode, len(t.Destinations))
	for i, dst := range t.Destinations {
		roots[i] = &funnel.TaskNode{
			Task: funnel.NewDestinationTask(dst.ID(), dst, logger, &funnel.NoOpConnectorMetrics{}),
		}
	}
	sink, err := funnel.NewSink(roots...)
	if err != nil {
		return nil, err
	}

	workers := make([]*funnel.Worker, len(t.Sources))
	for i, src := range t.Sources {
		first := &funnel.TaskNode{
			Task: funnel.NewSourceTask(src.ID(), src, logger, &funnel.NoOpConnectorMetrics{}),
		}
		if err := first.AppendToEnd(roots...); err != nil {
			return nil, err
		}
		d := t.dlq(src.ID())
		dlq := funnel.NewDLQ("dlq-"+src.ID(), d, logger, &funnel.NoOpConnectorMetrics{}, t.window(), t.DLQThreshold)
		w, err := funnel.NewWorker(first, dlq, logger, noop.Timer{})
		if err != nil {
			return nil, err
		}
		workers[i] = w
	}

	ctx, cancel := context.WithCancel(ctx)
	if err := sink.Open(ctx); err != nil {
		cancel()
		return nil, cerrors.Errorf("open shared sink: %w", err)
	}
	for i, w := range workers {
		if err := w.Open(ctx); err != nil {
			for _, o := range workers[:i] {
				_ = o.Close(context.Background())
			}
			_ = sink.Close(context.Background())
			cancel()
			return nil, cerrors.Errorf("open worker %d: %w", i, err)
		}
	}

	// Closes use a context that outlives pipeline cancellation, as the service
	// does: a killed run must still tear its connectors down.
	closeCtx := context.WithoutCancel(ctx)
	r := newRunner(cancel)
	for _, w := range workers {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			stopWatch := w.WatchConnectorErrors(ctx, r.fail)
			err := w.Do(ctx)
			stopWatch()
			r.fail(err)
			r.fail(w.Close(closeCtx))
		}()
	}
	r.stop = func(ctx context.Context) error {
		var errs []error
		for _, w := range workers {
			errs = append(errs, w.Stop(ctx))
		}
		return cerrors.Join(errs...)
	}
	r.start(func() {
		// Invariant 1/3: the shared sink is closed only after every worker has
		// exited, as Service.runPipeline does.
		r.fail(sink.Close(closeCtx))
		cancel()
	})
	return r, nil
}
