// Copyright © 2022 Meroxa, Inc.
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
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	connmock "github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/pipeline/stream"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/processor"
	procmock "github.com/conduitio/conduit/pkg/processor/mock"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/rs/zerolog"
)

func Example_simpleStream() {
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()

	logger := newLogger()
	ctrl := gomockCtrl(logger)

	node1 := &stream.SourceNode{
		Name:          "generator",
		Source:        generatorSource(ctrl, logger, "generator", 10, time.Millisecond*10),
		PipelineTimer: noop.Timer{},
	}
	node2 := &stream.SourceAckerNode{
		Name:   "generator-acker",
		Source: node1.Source,
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
	node3.AckerNode = node4

	stream.SetLogger(node1, logger)
	stream.SetLogger(node2, logger)
	stream.SetLogger(node3, logger)
	stream.SetLogger(node4, logger)

	// put everything together
	node2.Sub(node1.Pub())
	node3.Sub(node2.Pub())

	var wg sync.WaitGroup
	wg.Add(4)
	go runNode(ctx, &wg, node4)
	go runNode(ctx, &wg, node3)
	go runNode(ctx, &wg, node2)
	go runNode(ctx, &wg, node1)

	// stop node after 150ms, which should be enough to process the 10 messages
	time.AfterFunc(150*time.Millisecond, func() { node1.Stop(nil) })
	// give the node some time to process the records, plus a bit of time to stop
	if waitTimeout(&wg, 1000*time.Millisecond) {
		killAll()
	} else {
		logger.Info(ctx).Msg("finished successfully")
	}

	// Output:
	// DBG got record message_id=p/generator-1 node_id=printer
	// DBG received ack message_id=p/generator-1 node_id=generator
	// DBG got record message_id=p/generator-2 node_id=printer
	// DBG received ack message_id=p/generator-2 node_id=generator
	// DBG got record message_id=p/generator-3 node_id=printer
	// DBG received ack message_id=p/generator-3 node_id=generator
	// DBG got record message_id=p/generator-4 node_id=printer
	// DBG received ack message_id=p/generator-4 node_id=generator
	// DBG got record message_id=p/generator-5 node_id=printer
	// DBG received ack message_id=p/generator-5 node_id=generator
	// DBG got record message_id=p/generator-6 node_id=printer
	// DBG received ack message_id=p/generator-6 node_id=generator
	// DBG got record message_id=p/generator-7 node_id=printer
	// DBG received ack message_id=p/generator-7 node_id=generator
	// DBG got record message_id=p/generator-8 node_id=printer
	// DBG received ack message_id=p/generator-8 node_id=generator
	// DBG got record message_id=p/generator-9 node_id=printer
	// DBG received ack message_id=p/generator-9 node_id=generator
	// DBG got record message_id=p/generator-10 node_id=printer
	// DBG received ack message_id=p/generator-10 node_id=generator
	// INF stopping source connector component=SourceNode node_id=generator
	// DBG received error on error channel error="error reading from source: stream not open" component=SourceNode node_id=generator
	// DBG incoming messages channel closed component=SourceAckerNode node_id=generator-acker
	// DBG incoming messages channel closed component=DestinationNode node_id=printer
	// INF finished successfully
}

func Example_complexStream() {
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()

	logger := newLogger()
	ctrl := gomockCtrl(logger)

	var count int

	node1 := &stream.SourceNode{
		Name:          "generator1",
		Source:        generatorSource(ctrl, logger, "generator1", 10, time.Millisecond*10),
		PipelineTimer: noop.Timer{},
	}
	node2 := &stream.SourceAckerNode{
		Name:   "generator1-acker",
		Source: node1.Source,
	}
	node3 := &stream.SourceNode{
		Name:          "generator2",
		Source:        generatorSource(ctrl, logger, "generator2", 10, time.Millisecond*10),
		PipelineTimer: noop.Timer{},
	}
	node4 := &stream.SourceAckerNode{
		Name:   "generator2-acker",
		Source: node3.Source,
	}
	node5 := &stream.FaninNode{Name: "fanin"}
	node6 := &stream.ProcessorNode{
		Name:           "counter",
		Processor:      counterProcessor(ctrl, &count),
		ProcessorTimer: noop.Timer{},
	}
	node7 := &stream.FanoutNode{Name: "fanout"}
	node8 := &stream.DestinationNode{
		Name:           "printer1",
		Destination:    printerDestination(ctrl, logger, "printer1"),
		ConnectorTimer: noop.Timer{},
	}
	node9 := &stream.DestinationNode{
		Name:           "printer2",
		Destination:    printerDestination(ctrl, logger, "printer2"),
		ConnectorTimer: noop.Timer{},
	}
	node10 := &stream.DestinationAckerNode{
		Name:        "printer1-acker",
		Destination: node8.Destination,
	}
	node8.AckerNode = node10
	node11 := &stream.DestinationAckerNode{
		Name:        "printer2-acker",
		Destination: node9.Destination,
	}
	node9.AckerNode = node11

	// put everything together
	node2.Sub(node1.Pub())
	node4.Sub(node3.Pub())

	node5.Sub(node2.Pub())
	node5.Sub(node4.Pub())

	node6.Sub(node5.Pub())
	node7.Sub(node6.Pub())

	node8.Sub(node7.Pub())
	node9.Sub(node7.Pub())

	// run nodes
	nodes := []stream.Node{node1, node2, node3, node4, node5, node6, node7, node8, node9, node10, node11}

	var wg sync.WaitGroup
	wg.Add(len(nodes))
	for _, n := range nodes {
		stream.SetLogger(n, logger)
		go runNode(ctx, &wg, n)
	}

	// stop nodes after 250ms, which should be enough to process the 20 messages
	time.AfterFunc(
		250*time.Millisecond,
		func() {
			node1.Stop(nil)
			node3.Stop(nil)
		},
	)
	// give the nodes some time to process the records, plus a bit of time to stop
	if waitTimeout(&wg, 1000*time.Millisecond) {
		killAll()
	} else {
		logger.Info(ctx).Msgf("counter node counted %d messages", count)
		logger.Info(ctx).Msg("finished successfully")
	}

	// Unordered output:
	// DBG got record message_id=p/generator2-1 node_id=printer2
	// DBG got record message_id=p/generator2-1 node_id=printer1
	// DBG received ack message_id=p/generator2-1 node_id=generator2
	// DBG got record message_id=p/generator1-1 node_id=printer1
	// DBG got record message_id=p/generator1-1 node_id=printer2
	// DBG received ack message_id=p/generator1-1 node_id=generator1
	// DBG got record message_id=p/generator2-2 node_id=printer2
	// DBG got record message_id=p/generator2-2 node_id=printer1
	// DBG received ack message_id=p/generator2-2 node_id=generator2
	// DBG got record message_id=p/generator1-2 node_id=printer1
	// DBG got record message_id=p/generator1-2 node_id=printer2
	// DBG received ack message_id=p/generator1-2 node_id=generator1
	// DBG got record message_id=p/generator2-3 node_id=printer2
	// DBG got record message_id=p/generator2-3 node_id=printer1
	// DBG received ack message_id=p/generator2-3 node_id=generator2
	// DBG got record message_id=p/generator1-3 node_id=printer1
	// DBG got record message_id=p/generator1-3 node_id=printer2
	// DBG received ack message_id=p/generator1-3 node_id=generator1
	// DBG got record message_id=p/generator2-4 node_id=printer2
	// DBG got record message_id=p/generator2-4 node_id=printer1
	// DBG received ack message_id=p/generator2-4 node_id=generator2
	// DBG got record message_id=p/generator1-4 node_id=printer2
	// DBG got record message_id=p/generator1-4 node_id=printer1
	// DBG received ack message_id=p/generator1-4 node_id=generator1
	// DBG got record message_id=p/generator2-5 node_id=printer2
	// DBG got record message_id=p/generator2-5 node_id=printer1
	// DBG received ack message_id=p/generator2-5 node_id=generator2
	// DBG got record message_id=p/generator1-5 node_id=printer1
	// DBG got record message_id=p/generator1-5 node_id=printer2
	// DBG received ack message_id=p/generator1-5 node_id=generator1
	// DBG got record message_id=p/generator2-6 node_id=printer2
	// DBG got record message_id=p/generator2-6 node_id=printer1
	// DBG received ack message_id=p/generator2-6 node_id=generator2
	// DBG got record message_id=p/generator1-6 node_id=printer1
	// DBG got record message_id=p/generator1-6 node_id=printer2
	// DBG received ack message_id=p/generator1-6 node_id=generator1
	// DBG got record message_id=p/generator2-7 node_id=printer2
	// DBG got record message_id=p/generator2-7 node_id=printer1
	// DBG received ack message_id=p/generator2-7 node_id=generator2
	// DBG got record message_id=p/generator1-7 node_id=printer1
	// DBG got record message_id=p/generator1-7 node_id=printer2
	// DBG received ack message_id=p/generator1-7 node_id=generator1
	// DBG got record message_id=p/generator2-8 node_id=printer2
	// DBG got record message_id=p/generator2-8 node_id=printer1
	// DBG received ack message_id=p/generator2-8 node_id=generator2
	// DBG got record message_id=p/generator1-8 node_id=printer1
	// DBG got record message_id=p/generator1-8 node_id=printer2
	// DBG received ack message_id=p/generator1-8 node_id=generator1
	// DBG got record message_id=p/generator2-9 node_id=printer1
	// DBG got record message_id=p/generator2-9 node_id=printer2
	// DBG received ack message_id=p/generator2-9 node_id=generator2
	// DBG got record message_id=p/generator1-9 node_id=printer2
	// DBG got record message_id=p/generator1-9 node_id=printer1
	// DBG received ack message_id=p/generator1-9 node_id=generator1
	// DBG got record message_id=p/generator2-10 node_id=printer1
	// DBG got record message_id=p/generator2-10 node_id=printer2
	// DBG received ack message_id=p/generator2-10 node_id=generator2
	// DBG got record message_id=p/generator1-10 node_id=printer2
	// DBG got record message_id=p/generator1-10 node_id=printer1
	// DBG received ack message_id=p/generator1-10 node_id=generator1
	// INF stopping source connector component=SourceNode node_id=generator1
	// INF stopping source connector component=SourceNode node_id=generator2
	// DBG incoming messages channel closed component=SourceAckerNode node_id=generator1-acker
	// DBG incoming messages channel closed component=SourceAckerNode node_id=generator2-acker
	// DBG received error on error channel error="error reading from source: stream not open" component=SourceNode node_id=generator1
	// DBG received error on error channel error="error reading from source: stream not open" component=SourceNode node_id=generator2
	// DBG incoming messages channel closed component=ProcessorNode node_id=counter
	// DBG incoming messages channel closed component=DestinationNode node_id=printer2
	// DBG incoming messages channel closed component=DestinationNode node_id=printer1
	// INF counter node counted 20 messages
	// INF finished successfully
}

func newLogger() log.CtxLogger {
	w := zerolog.NewConsoleWriter()
	w.NoColor = true
	w.PartsExclude = []string{zerolog.TimestampFieldName}

	zlogger := zerolog.New(w)
	zlogger = zlogger.Level(zerolog.DebugLevel)
	logger := log.New(zlogger)
	logger = logger.CtxHook(ctxutil.MessageIDLogCtxHook{})

	return logger
}

func generatorSource(ctrl *gomock.Controller, logger log.CtxLogger, nodeID string, recordCount int, delay time.Duration) connector.Source {
	position := 0

	stop := make(chan struct{})
	source := connmock.NewSource(ctrl)
	source.EXPECT().Open(gomock.Any()).Return(nil).Times(1)
	source.EXPECT().Teardown(gomock.Any()).Return(nil).Times(1)
	source.EXPECT().Ack(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, p record.Position) error {
		logger.Debug(ctx).Str("node_id", nodeID).Msg("received ack")
		return nil
	}).Times(recordCount)
	source.EXPECT().Read(gomock.Any()).DoAndReturn(func(ctx context.Context) (record.Record, error) {
		time.Sleep(delay)

		position++
		if position > recordCount {
			// block until Stop is called
			<-stop
			return record.Record{}, plugin.ErrStreamNotOpen
		}

		return record.Record{
			// SourceID would normally be the source node ID, but since we need
			// to add the node ID to the position to create unique positions we
			// just use "p" here to create a nicer test output
			SourceID: "p",
			Position: record.Position(nodeID + "-" + strconv.Itoa(position)),
		}, nil
	}).MinTimes(recordCount + 1)
	source.EXPECT().Stop(gomock.Any()).DoAndReturn(func(context.Context) error {
		close(stop)
		return nil
	})
	source.EXPECT().Errors().Return(make(chan error))

	return source
}

func printerDestination(ctrl *gomock.Controller, logger log.CtxLogger, nodeID string) connector.Destination {
	rchan := make(chan record.Record)
	destination := connmock.NewDestination(ctrl)
	destination.EXPECT().Open(gomock.Any()).Return(nil).Times(1)
	destination.EXPECT().Write(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, r record.Record) error {
		logger.Debug(ctx).
			Str("node_id", nodeID).
			Msg("got record")
		rchan <- r
		return nil
	}).AnyTimes()
	destination.EXPECT().Ack(gomock.Any()).DoAndReturn(func(ctx context.Context) (record.Position, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r, ok := <-rchan:
			if !ok {
				return nil, nil
			}
			return r.Position, nil
		}
	}).AnyTimes()
	destination.EXPECT().Teardown(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
		close(rchan)
		return nil
	}).Times(1)
	destination.EXPECT().Errors().Return(make(chan error))

	return destination
}

func counterProcessor(ctrl *gomock.Controller, count *int) processor.Interface {
	proc := procmock.NewProcessor(ctrl)
	proc.EXPECT().Process(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, r record.Record) (record.Record, error) {
		*count++
		return r, nil
	}).AnyTimes()
	return proc
}

func gomockCtrl(logger log.CtxLogger) *gomock.Controller {
	return gomock.NewController(gomockLogger(logger))
}

type gomockLogger log.CtxLogger

func (g gomockLogger) Errorf(format string, args ...interface{}) {
	g.Error().Msgf(format, args...)
}
func (g gomockLogger) Fatalf(format string, args ...interface{}) {
	g.Fatal().Msgf(format, args...)
}

func runNode(ctx context.Context, wg *sync.WaitGroup, n stream.Node) {
	defer wg.Done()
	err := n.Run(ctx)
	if err != nil {
		fmt.Printf("%s error: %v\n", n.ID(), err)
	}
}

func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false // completed normally
	case <-time.After(timeout):
		return true // timed out
	}
}
