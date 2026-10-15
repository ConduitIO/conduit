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
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
)

// V1 drives pkg/lifecycle.
type V1 struct{}

// Name implements Engine.
func (V1) Name() string { return "v1" }

// Start implements Engine. It builds the node graph the v1 service builds:
//
//	source -> source acker -\                      /-> destination -> destination acker
//	                         |- fan-in -> fan-out -|
//	source -> source acker -/                      \-> destination -> destination acker
//
// with one DLQ handler node that every source acker reports to.
func (V1) Start(ctx context.Context, t Topology) (Run, error) {
	if len(t.Sources) == 0 || len(t.Destinations) == 0 {
		return nil, cerrors.New("engines: need at least one source and one destination")
	}
	ctx, cancel := context.WithCancel(ctx)
	logger := log.Nop()

	dlq := &stream.DLQHandlerNode{
		Name:                "dlq",
		Handler:             &lifecycle.DLQDestination{Destination: t.dlq(""), Logger: logger},
		WindowSize:          t.window(),
		WindowNackThreshold: t.DLQThreshold,
		Timer:               noop.Timer{},
		Histogram:           metrics.NewRecordBytesHistogram(noop.Histogram{}),
	}
	dlq.Add(len(t.Sources))

	nodes := []stream.Node{dlq}
	sourceNodes := make([]*stream.SourceNode, 0, len(t.Sources))
	fanin := &stream.FaninNode{Name: "fanin"}
	fanout := &stream.FanoutNode{Name: "fanout"}

	for _, src := range t.Sources {
		sn := &stream.SourceNode{Name: src.ID(), Source: src, PipelineTimer: noop.Timer{}}
		an := &stream.SourceAckerNode{Name: src.ID() + "-acker", Source: src, DLQHandlerNode: dlq}
		an.Sub(sn.Pub())
		fanin.Sub(an.Pub())
		sourceNodes = append(sourceNodes, sn)
		nodes = append(nodes, sn, an)
	}
	fanout.Sub(fanin.Pub())
	nodes = append(nodes, fanin, fanout)

	for _, dst := range t.Destinations {
		dn := &stream.DestinationNode{Name: dst.ID(), Destination: dst, ConnectorTimer: noop.Timer{}}
		da := &stream.DestinationAckerNode{Name: dst.ID() + "-acker", Destination: dst}
		dn.Sub(fanout.Pub())
		da.Sub(dn.Pub())
		nodes = append(nodes, dn, da)
	}

	r := newRunner(cancel)
	for _, n := range nodes {
		stream.SetLogger(n, logger)
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.fail(n.Run(ctx))
		}()
	}
	r.stop = func(ctx context.Context) error {
		var errs []error
		for _, sn := range sourceNodes {
			errs = append(errs, sn.Stop(ctx, nil))
		}
		return cerrors.Join(errs...)
	}
	r.start(cancel)
	return r, nil
}
