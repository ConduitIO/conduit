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
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
)

// memV1Destination implements stream.Destination as a synthetic in-memory
// sink, mirroring pkg/lifecycle/stream/stream_test.go's printerDestination
// but as a plain struct instead of a gomock so it can be reused (not
// expectation-scripted) across every v1 shape test.
type memV1Destination struct {
	id    string
	rchan chan opencdc.Record

	mu           sync.Mutex
	delivered    []opencdc.Record
	stopPosition opencdc.Position
}

func newMemV1Destination(id string, buf int) *memV1Destination {
	return &memV1Destination{id: id, rchan: make(chan opencdc.Record, buf)}
}

func (d *memV1Destination) ID() string                 { return d.id }
func (d *memV1Destination) Open(context.Context) error { return nil }
func (d *memV1Destination) Errors() <-chan error       { return make(chan error) }

func (d *memV1Destination) Write(_ context.Context, recs []opencdc.Record) error {
	d.mu.Lock()
	d.delivered = append(d.delivered, recs...)
	d.mu.Unlock()
	for _, r := range recs {
		d.rchan <- r
	}
	return nil
}

func (d *memV1Destination) Ack(ctx context.Context) ([]connector.DestinationAck, error) {
	select {
	case r, ok := <-d.rchan:
		if !ok {
			return nil, nil
		}
		return []connector.DestinationAck{{Position: r.Position}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *memV1Destination) Stop(_ context.Context, pos opencdc.Position) error {
	d.mu.Lock()
	d.stopPosition = pos
	d.mu.Unlock()
	return nil
}

func (d *memV1Destination) Teardown(context.Context) error {
	close(d.rchan)
	return nil
}

func (d *memV1Destination) positions() []opencdc.Position {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]opencdc.Position, len(d.delivered))
	for i, r := range d.delivered {
		out[i] = r.Position
	}
	return out
}

func (d *memV1Destination) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.delivered)
}

func (d *memV1Destination) hasPosition(pos opencdc.Position) bool {
	for _, p := range d.positions() {
		if string(p) == string(pos) {
			return true
		}
	}
	return false
}

// memV1DLQHandler implements stream.DLQHandler as a synthetic in-memory
// recorder.
type memV1DLQHandler struct {
	mu      sync.Mutex
	written []opencdc.Record
}

func (h *memV1DLQHandler) Open(context.Context) error { return nil }

func (h *memV1DLQHandler) Write(_ context.Context, r opencdc.Record) error {
	h.mu.Lock()
	h.written = append(h.written, r)
	h.mu.Unlock()
	return nil
}

func (h *memV1DLQHandler) Close(context.Context) error { return nil }

func (h *memV1DLQHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.written)
}

// v1Pipeline wires the real pkg/lifecycle/stream node graph for the
// batch-shape suite: SourceNode -> SourceAckerNode -> [ProcessorNode] ->
// DestinationNode -> DestinationAckerNode, with a real DLQHandlerNode.
// Mirrors pkg/lifecycle/stream/stream_test.go's Example_simpleStream, but
// driven by a real *connector.Source (sourceHarness) instead of a mock, and
// run to completion instead of on a fixed timer.
type v1Pipeline struct {
	t          *testing.T
	sourceNode *stream.SourceNode
	dest       *memV1Destination
	dlq        *memV1DLQHandler

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu   sync.Mutex
	errs map[string]error
}

// v1Config configures newV1Pipeline. Proc may be nil for a pure passthrough
// (no processor node in the chain).
type v1Config struct {
	Proc                stream.Processor
	DLQWindowSize       int
	DLQWindowNackThresh int
}

func newV1Pipeline(t *testing.T, sh *sourceHarness, cfg v1Config) *v1Pipeline {
	t.Helper()
	logger := log.Test(t)
	ctx, cancel := context.WithCancel(context.Background())

	dlqHandler := &memV1DLQHandler{}
	dlqNode := &stream.DLQHandlerNode{
		Name:                "dlq",
		Handler:             dlqHandler,
		WindowSize:          cfg.DLQWindowSize,
		WindowNackThreshold: cfg.DLQWindowNackThresh,
		Timer:               noop.Timer{},
		Histogram:           metrics.NewRecordBytesHistogram(noop.Histogram{}),
	}
	dlqNode.Add(1) // 1 source

	srcNode := &stream.SourceNode{Name: "source", Source: sh.Source, PipelineTimer: noop.Timer{}}
	ackerNode := &stream.SourceAckerNode{Name: "source-acker", Source: srcNode.Source, DLQHandlerNode: dlqNode}
	ackerNode.Sub(srcNode.Pub())

	dest := newMemV1Destination("dest", 64)
	destNode := &stream.DestinationNode{Name: "dest", Destination: dest, ConnectorTimer: noop.Timer{}}
	destAckerNode := &stream.DestinationAckerNode{Name: "dest-acker", Destination: dest}

	allNodes := []stream.Node{dlqNode, srcNode, ackerNode}

	var lastPub stream.PubNode = ackerNode
	if cfg.Proc != nil {
		procNode := &stream.ProcessorNode{Name: "proc", Processor: cfg.Proc, ProcessorTimer: noop.Timer{}}
		procNode.Sub(lastPub.Pub())
		lastPub = procNode
		allNodes = append(allNodes, procNode)
	}

	destNode.Sub(lastPub.Pub())
	destAckerNode.Sub(destNode.Pub())
	allNodes = append(allNodes, destNode, destAckerNode)

	for _, n := range allNodes {
		if ln, ok := n.(stream.LoggingNode); ok {
			ln.SetLogger(logger)
		}
	}

	p := &v1Pipeline{
		t:          t,
		sourceNode: srcNode,
		dest:       dest,
		dlq:        dlqHandler,
		ctx:        ctx,
		cancel:     cancel,
		errs:       make(map[string]error),
	}

	p.wg.Add(len(allNodes))
	for _, n := range allNodes {
		go func(n stream.Node) {
			defer p.wg.Done()
			if err := n.Run(p.ctx); err != nil {
				p.mu.Lock()
				p.errs[n.ID()] = err
				p.mu.Unlock()
				// Mimic production orchestration (pkg/lifecycle.Service):
				// one node's fatal error tears down the whole pipeline. These
				// primitive nodes do not cross-cancel each other on their
				// own (a downstream node returning nil on a closed inbound
				// channel is the ONLY built-in cascade - see
				// DestinationNode.Run) - without this, a node upstream of the
				// failure (e.g. SourceAckerNode blocked sending to a
				// processor that already exited) would deadlock forever
				// instead of unwinding.
				cancel()
			}
		}(n)
	}

	// Arm graceful drain immediately. Safe even racing production: the
	// control message this injects is just another entry in the same
	// message queue as real records (pkg/lifecycle/stream/base.go's
	// InjectControlMessage), and SourceNode.Run only terminates once it has
	// actually forwarded the record matching the stop position - not merely
	// received the control message (see source.go's Run loop and
	// seqPlugin.Stop's doc comment).
	_ = srcNode.Stop(ctx, nil)

	return p
}

// waitDone waits (bounded) for every node's Run to return, force-cancelling
// the pipeline context if the timeout is hit. Returns a copy of every
// non-nil error observed, keyed by node ID.
func (p *v1Pipeline) waitDone(timeout time.Duration) map[string]error {
	p.t.Helper()
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		p.cancel()
		<-done
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]error, len(p.errs))
	for k, v := range p.errs {
		out[k] = v
	}
	return out
}

func fmtErrs(errs map[string]error) string {
	return fmt.Sprintf("%v", errs)
}
