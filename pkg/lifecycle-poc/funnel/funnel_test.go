// Copyright © 2024 Meroxa, Inc.
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
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/csync"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

func Example_simpleStream() {
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()

	logger := newLogger()
	ctrl := gomockCtrl(logger)

	batchCount := 10
	batchSize := 1

	dlq := NewDLQ(
		"dlq",
		noopDLQDestination(ctrl),
		logger,
		&NoOpConnectorMetrics{},
		1,
		0,
	)
	srcTask := NewSourceTask(
		"generator",
		generatorSource(ctrl, logger, "generator", batchSize, batchCount),
		logger,
		&NoOpConnectorMetrics{},
	)
	destTask := NewDestinationTask(
		"printer",
		printerDestination(ctrl, logger, "printer", batchSize),
		logger,
		&NoOpConnectorMetrics{},
	)

	destTaskNode := &TaskNode{Task: destTask}
	srcTaskNode := &TaskNode{Task: srcTask, Next: []*TaskNode{destTaskNode}}

	w, err := NewWorker(
		srcTaskNode,
		dlq,
		logger,
		noop.Timer{},
	)
	if err != nil {
		panic(err)
	}

	err = w.Open(ctx)
	if err != nil {
		panic(err)
	}

	var wg csync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := w.Do(ctx)
		if err != nil {
			panic(err)
		}
	}()

	// stop node after 150ms, which should be enough to process the 10 messages
	time.AfterFunc(150*time.Millisecond, func() { _ = w.Stop(ctx) })

	if err := wg.WaitTimeout(ctx, time.Second); err != nil {
		killAll()
	} else {
		logger.Info(ctx).Msg("finished successfully")
	}

	err = w.Close(ctx)
	if err != nil {
		panic(err)
	}

	// Output:
	// DBG opening source component=task:source connector_id=generator
	// DBG source open component=task:source connector_id=generator
	// DBG opening destination component=task:destination connector_id=printer
	// DBG destination open component=task:destination connector_id=printer
	// DBG opening destination component=task:destination connector_id=dlq
	// DBG destination open component=task:destination connector_id=dlq
	// DBG got record node_id=printer position=generator/0
	// DBG received ack node_id=generator
	// DBG got record node_id=printer position=generator/1
	// DBG received ack node_id=generator
	// DBG got record node_id=printer position=generator/2
	// DBG received ack node_id=generator
	// DBG got record node_id=printer position=generator/3
	// DBG received ack node_id=generator
	// DBG got record node_id=printer position=generator/4
	// DBG received ack node_id=generator
	// DBG got record node_id=printer position=generator/5
	// DBG received ack node_id=generator
	// DBG got record node_id=printer position=generator/6
	// DBG received ack node_id=generator
	// DBG got record node_id=printer position=generator/7
	// DBG received ack node_id=generator
	// DBG got record node_id=printer position=generator/8
	// DBG received ack node_id=generator
	// DBG got record node_id=printer position=generator/9
	// DBG received ack node_id=generator
	// INF finished successfully
}

func BenchmarkStreamNew(b *testing.B) {
	ctx, killAll := context.WithCancel(context.Background())
	defer killAll()

	logger := log.Nop()
	ctrl := gomockCtrl(logger)

	b.ReportAllocs()
	b.StopTimer()
	for i := 0; i < b.N; i++ {
		batchCount := 100
		batchSize := 1000

		dlq := NewDLQ(
			"dlq",
			noopDLQDestination(ctrl),
			logger,
			&NoOpConnectorMetrics{},
			1,
			0,
		)
		srcTask := NewSourceTask(
			"generator",
			generatorSource(ctrl, logger, "generator", batchSize, batchCount),
			logger,
			&NoOpConnectorMetrics{},
		)
		destTask := NewDestinationTask(
			"printer",
			printerDestination(ctrl, logger, "printer", batchSize),
			logger,
			&NoOpConnectorMetrics{},
		)

		destTaskNode := &TaskNode{Task: destTask}
		srcTaskNode := &TaskNode{Task: srcTask, Next: []*TaskNode{destTaskNode}}

		w, err := NewWorker(
			srcTaskNode,
			dlq,
			logger,
			noop.Timer{},
		)
		if err != nil {
			panic(err)
		}

		b.StartTimer()

		var wg csync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := w.Do(ctx)
			if err != nil {
				panic(err)
			}
		}()

		// stop node after 150ms, which should be enough to process the 10 messages
		time.AfterFunc(150*time.Millisecond, func() { _ = w.Stop(ctx) })

		if err := wg.WaitTimeout(ctx, time.Second); err != nil {
			killAll()
		}

		err = w.Close(ctx)
		if err != nil {
			panic(err)
		}

		b.StopTimer()
	}
}

func newLogger() log.CtxLogger {
	w := zerolog.NewConsoleWriter()
	w.NoColor = true
	w.PartsExclude = []string{zerolog.TimestampFieldName}

	zlogger := zerolog.New(w)
	zlogger = zlogger.Level(zerolog.DebugLevel)
	logger := log.New(zlogger)
	logger.Logger = logger.Hook(ctxutil.MessageIDLogCtxHook{})

	return logger
}

func generatorSource(ctrl *gomock.Controller, logger log.CtxLogger, nodeID string, batchSize, batchCount int) Source {
	position := 0

	teardown := make(chan struct{})
	source := NewMockSource(ctrl)
	source.EXPECT().ID().Return(nodeID).AnyTimes()
	source.EXPECT().Open(gomock.Any()).Return(nil)
	source.EXPECT().Teardown(gomock.Any()).DoAndReturn(func(context.Context) error {
		close(teardown)
		return nil
	})
	source.EXPECT().Ack(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, p []opencdc.Position) error {
		logger.Debug(ctx).Str("node_id", nodeID).Msg("received ack")
		return nil
	}).Times(batchCount * batchSize)
	source.EXPECT().Read(gomock.Any()).DoAndReturn(func(ctx context.Context) ([]opencdc.Record, error) {
		if position == batchCount*batchSize {
			// block until Teardown is called
			<-teardown
			return nil, context.Canceled
		}

		recs := make([]opencdc.Record, batchSize)
		for i := 0; i < batchSize; i++ {
			recs[i] = opencdc.Record{
				Metadata: opencdc.Metadata{
					opencdc.MetadataConduitSourceConnectorID: nodeID,
				},
				Position: opencdc.Position(strconv.Itoa(position)),
			}
			position++
		}

		return recs, nil
	}).MinTimes(batchCount + 1)
	source.EXPECT().Errors().Return(make(chan error))

	return source
}

func printerDestination(ctrl *gomock.Controller, logger log.CtxLogger, nodeID string, batchSize int) Destination {
	var lastPosition opencdc.Position
	_ = lastPosition
	rchan := make(chan opencdc.Record, batchSize)
	destination := NewMockDestination(ctrl)
	destination.EXPECT().Open(gomock.Any()).Return(nil)
	destination.EXPECT().Write(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, recs []opencdc.Record) error {
		for _, r := range recs {
			connID, _ := r.Metadata.GetConduitSourceConnectorID()
			logger.Debug(ctx).
				Str("position", fmt.Sprintf("%s/%s", connID, r.Position)).
				Str("node_id", nodeID).
				Msg("got record")
			lastPosition = r.Position
			rchan <- r
		}
		return nil
	}).AnyTimes()
	destination.EXPECT().Ack(gomock.Any()).DoAndReturn(func(ctx context.Context) ([]connector.DestinationAck, error) {
		acks := make([]connector.DestinationAck, 0, batchSize)
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case r, ok := <-rchan:
				if !ok {
					return nil, nil
				}
				acks = append(acks, connector.DestinationAck{Position: r.Position})
			default:
				return acks, nil
			}
		}
	}).AnyTimes()
	destination.EXPECT().Teardown(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
		close(rchan)
		return nil
	})
	destination.EXPECT().Errors().Return(make(chan error))

	return destination
}

func noopDLQDestination(ctrl *gomock.Controller) Destination {
	destination := NewMockDestination(ctrl)
	destination.EXPECT().Open(gomock.Any()).Return(nil)
	destination.EXPECT().Teardown(gomock.Any()).Return(nil)
	return destination
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
