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

package upgrade

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
)

// memDestination is a synthetic in-memory funnel.Destination. Write appends
// every written record (in order) and queues a matching successful ack;
// Ack drains the queue - matching DestinationTask.Do's polling contract
// (pkg/lifecycle-poc/funnel/destination.go): one Write, then repeated Ack
// calls until every written position has been acknowledged. Mirrors
// tests/chaos/property4_test.go's synthDestination.
type memDestination struct {
	id string

	mu        sync.Mutex
	delivered []opencdc.Record
	pending   []connector.DestinationAck
}

func newMemDestination(id string) *memDestination { return &memDestination{id: id} }

func (d *memDestination) ID() string                     { return d.id }
func (d *memDestination) Open(context.Context) error     { return nil }
func (d *memDestination) Teardown(context.Context) error { return nil }
func (d *memDestination) Errors() <-chan error           { return make(chan error) }

func (d *memDestination) Write(_ context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range recs {
		d.delivered = append(d.delivered, r)
		d.pending = append(d.pending, connector.DestinationAck{Position: r.Position})
	}
	return nil
}

func (d *memDestination) Ack(context.Context) ([]connector.DestinationAck, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	acks := d.pending
	d.pending = nil
	return acks, nil
}

func (d *memDestination) positions() []opencdc.Position {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]opencdc.Position, len(d.delivered))
	for i, r := range d.delivered {
		out[i] = r.Position
	}
	return out
}

func (d *memDestination) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.delivered)
}

func (d *memDestination) hasPosition(pos opencdc.Position) bool {
	for _, p := range d.positions() {
		if string(p) == string(pos) {
			return true
		}
	}
	return false
}

// hasPositionSuffix reports whether a delivered record's position equals
// base+suffix - a convenience for asserting on splitPieces' "2a"/"2b"/"2c"
// style positions.
func (d *memDestination) hasPositionSuffix(base, suffix string) bool {
	return d.hasPosition(opencdc.Position(base + suffix))
}

// v2Pipeline wires a funnel.Worker for the batch-shape suite: SourceTask
// (wrapping a real sourceHarness.Source) -> middle task(s) (the shape under
// test) -> destination task(s) (1 for a linear pipeline, M for fan-out),
// with a real funnel.DLQ. Mirrors tests/chaos/property4_test.go's
// funnelHarness.
type v2Pipeline struct {
	t      *testing.T
	worker *funnel.Worker
	doErr  chan error
}

// v2Config configures newV2Pipeline. Middle is the ordered chain of tasks
// between the source and the destination fan-out point (may be empty for a
// pure passthrough). Dests must have at least one entry; more than one means
// a destination fan-out (see pkg/lifecycle-poc/funnel/worker.go's
// doNextTask). DLQWindowSize/DLQWindowNackThreshold select the DLQ policy,
// per funnel.DLQ's real dlqWindow semantics (0,0 = DLQ always accepts;
// windowSize=1,threshold=0 = DLQ disabled, any nack halts the pipeline - see
// tests/chaos/property4_test.go's newFunnelHarness doc for the precise
// mechanics).
type v2Config struct {
	Middle              []funnel.Task
	Dests               []*memDestination
	DLQDest             *memDestination
	DLQWindowSize       int
	DLQWindowNackThresh int
}

func newV2Pipeline(t *testing.T, sh *sourceHarness, cfg v2Config) *v2Pipeline {
	t.Helper()
	if len(cfg.Dests) == 0 {
		t.Fatal("v2Config.Dests must have at least one destination")
	}
	logger := log.Test(t)

	srcTask := funnel.NewSourceTask("source", sh.Source, logger, funnel.NoOpConnectorMetrics{})
	srcNode := &funnel.TaskNode{Task: srcTask}

	cur := srcNode
	for _, task := range cfg.Middle {
		n := &funnel.TaskNode{Task: task}
		cur.Next = []*funnel.TaskNode{n}
		cur = n
	}

	destNodes := make([]*funnel.TaskNode, len(cfg.Dests))
	for i, d := range cfg.Dests {
		dt := funnel.NewDestinationTask(fmt.Sprintf("dest-%d", i), d, logger, funnel.NoOpConnectorMetrics{})
		destNodes[i] = &funnel.TaskNode{Task: dt}
	}
	cur.Next = destNodes

	dlqDest := cfg.DLQDest
	if dlqDest == nil {
		dlqDest = newMemDestination("dlq")
	}
	dlq := funnel.NewDLQ("dlq", dlqDest, logger, funnel.NoOpConnectorMetrics{}, cfg.DLQWindowSize, cfg.DLQWindowNackThresh)

	worker, err := funnel.NewWorker(srcNode, dlq, logger, noop.Timer{})
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	if err := worker.Open(context.Background()); err != nil {
		t.Fatalf("worker open: %v", err)
	}

	p := &v2Pipeline{t: t, worker: worker, doErr: make(chan error, 1)}
	go func() { p.doErr <- worker.Do(context.Background()) }()
	return p
}

// waitTotalDelivered polls until the combined record count across dests
// reaches at least n, or fails the test after timeout.
func (p *v2Pipeline) waitTotalDelivered(n int, timeout time.Duration, dests ...*memDestination) {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		total := 0
		for _, d := range dests {
			total += d.count()
		}
		if total >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	total := 0
	for _, d := range dests {
		total += d.count()
	}
	p.t.Fatalf("timed out waiting for %d records delivered (observed %d)", n, total)
}

// waitEachDelivered polls until EVERY destination has independently
// delivered at least n records - the fan-out shape (broadcast, not
// partition): every destination should see the full record set.
func (p *v2Pipeline) waitEachDelivered(n int, timeout time.Duration, dests ...*memDestination) {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allDone := true
		for _, d := range dests {
			if d.count() < n {
				allDone = false
				break
			}
		}
		if allDone {
			return
		}
		time.Sleep(time.Millisecond)
	}
	for i, d := range dests {
		p.t.Logf("dest[%d]=%q delivered=%d", i, d.id, d.count())
	}
	p.t.Fatalf("timed out waiting for every destination to deliver %d records", n)
}

// stopGracefully stops the worker (draining any in-flight batch, tearing
// down the source) and waits for the Do goroutine to return, asserting it
// returned cleanly (no error). Mirrors
// tests/chaos/property4_test.go's funnelHarness.stopGracefully.
func (p *v2Pipeline) stopGracefully(timeout time.Duration) {
	p.t.Helper()
	if err := p.worker.Stop(context.Background()); err != nil {
		p.t.Fatalf("worker stop: %v", err)
	}
	select {
	case err := <-p.doErr:
		if err != nil {
			p.t.Fatalf("worker.Do returned an error after a graceful Stop: %v", err)
		}
	case <-time.After(timeout):
		p.t.Fatal("timed out waiting for worker.Do to return after Stop")
	}
	if err := p.worker.Close(context.Background()); err != nil {
		p.t.Fatalf("worker close: %v", err)
	}
}

// waitFatal waits for the Do goroutine to return an error (the halt case:
// doTask propagates a fatal error up through Do without Stop ever being
// called), then closes the worker so the source is torn down. Mirrors
// tests/chaos/property4_test.go's funnelHarness.waitHalted.
func (p *v2Pipeline) waitFatal(timeout time.Duration) error {
	p.t.Helper()
	select {
	case err := <-p.doErr:
		if err == nil {
			p.t.Fatal("expected worker.Do to return a fatal error, got nil")
		}
		if closeErr := p.worker.Close(context.Background()); closeErr != nil {
			p.t.Fatalf("worker close: %v", closeErr)
		}
		return err
	case <-time.After(timeout):
		p.t.Fatal("timed out waiting for worker.Do to halt")
		return nil // unreachable
	}
}
