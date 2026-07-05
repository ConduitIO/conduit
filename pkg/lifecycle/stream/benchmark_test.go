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

package stream_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
)

// BenchmarkStreamOld measures the v1 (record-at-a-time node graph) data path
// moving records generator -> printer, mirroring BenchmarkStreamNew in the v2
// funnel package (which processes batchCount*batchSize = 100_000 records). Both
// process exactly recordCount records (enforced by the source Ack expectation),
// so allocs/op and B/op are directly comparable across v1 and v2; ns/op is
// dominated by the stop window and is NOT a throughput measure.
func BenchmarkStreamOld(b *testing.B) {
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()

	logger := log.Nop()

	b.ReportAllocs()
	b.StopTimer()
	for i := 0; i < b.N; i++ {
		const recordCount = 100000

		ctrl := gomockCtrl(logger)
		dlqNode := &stream.DLQHandlerNode{
			Name:                "dlq",
			Handler:             noopDLQHandler(ctrl),
			WindowSize:          1,
			WindowNackThreshold: 0,
		}
		dlqNode.Add(1) // 1 source
		node1 := &stream.SourceNode{
			Name:          "generator",
			Source:        generatorSource(ctrl, logger, "generator", recordCount, 0),
			PipelineTimer: noop.Timer{},
		}
		node2 := &stream.SourceAckerNode{
			Name:           "generator-acker",
			Source:         node1.Source,
			DLQHandlerNode: dlqNode,
		}
		node3 := &stream.DestinationNode{
			Name:           "printer",
			Destination:    printerDestination(ctrl, logger, "printer"),
			ConnectorTimer: noop.Timer{},
		}
		node4 := &stream.DestinationAckerNode{
			Name:        "printer-acker",
			Destination: node3.Destination,
		}
		stream.SetLogger(node1, logger)
		stream.SetLogger(node2, logger)
		stream.SetLogger(node3, logger)
		stream.SetLogger(node4, logger)
		node2.Sub(node1.Pub())
		node3.Sub(node2.Pub())
		node4.Sub(node3.Pub())

		b.StartTimer()
		var wg sync.WaitGroup
		wg.Add(5)
		go runNode(ctx, &wg, dlqNode)
		go runNode(ctx, &wg, node4)
		go runNode(ctx, &wg, node3)
		go runNode(ctx, &wg, node2)
		go runNode(ctx, &wg, node1)

		// Stop the source after all records have streamed through (recordCount
		// completes well within the window). Because the source blocks once
		// drained, the window only bounds ns/op — it never processes more than
		// recordCount, so allocs/op and B/op measure the cost of exactly
		// recordCount records and are comparable to BenchmarkStreamNew.
		time.AfterFunc(3*time.Second, func() { _ = node1.Stop(ctx, nil) })
		if (*csync.WaitGroup)(&wg).WaitTimeout(ctx, 10*time.Second) != nil {
			killAll()
		}
		b.StopTimer()
	}
}
