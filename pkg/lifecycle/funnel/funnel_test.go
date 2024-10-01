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

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/noop"
	"github.com/conduitio/conduit/pkg/lifecycle/stream"
	streammock "github.com/conduitio/conduit/pkg/lifecycle/stream/mock"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
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
		noop.Timer{},
		noop.Histogram{},
		1,
		0,
	)
	srcTask := NewSourceTask(
		"generator",
		generatorSource(ctrl, logger, "generator", batchSize, batchCount),
		logger,
	)
	destTask := NewDestinationTask(
		"printer",
		printerDestination(ctrl, logger, "printer", batchSize),
		logger,
		noop.Timer{},
		noop.Histogram{},
	)

	w, err := NewWorker(
		[]Task{srcTask, destTask},
		[][]int{{1}, {}},
		dlq,
		logger,
	)
	if err != nil {
		panic(err)
	}

	err = w.Open(ctx)
	if err != nil {
		panic(err)
	}
	for i := 0; i < batchCount; i++ {
		err := w.Do(ctx)
		if err != nil {
			panic(err)
		}
	}

	err = w.Close(ctx)
	if err != nil {
		panic(err)
	}

	logger.Info(ctx).Msg("finished successfully")

	// Output:
	// DBG opening source component=task:source connector_id=generator
	// DBG source open component=task:source connector_id=generator
	// DBG opening destination component=task:destination connector_id=printer
	// DBG destination open component=task:destination connector_id=printer
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
			noop.Timer{},
			noop.Histogram{},
			1,
			0,
		)
		srcTask := NewSourceTask(
			"generator",
			generatorSource(ctrl, logger, "generator", batchSize, batchCount),
			logger,
		)
		destTask := NewDestinationTask(
			"printer",
			printerDestination(ctrl, logger, "printer", batchSize),
			logger,
			noop.Timer{},
			noop.Histogram{},
		)

		w, err := NewWorker(
			[]Task{srcTask, destTask},
			[][]int{{1}, {}},
			dlq,
			logger,
		)
		if err != nil {
			panic(err)
		}

		b.StartTimer()

		err = w.Open(ctx)
		if err != nil {
			panic(err)
		}

		for i := 0; i < batchCount; i++ {
			err := w.Do(ctx)
			if err != nil {
				panic(err)
			}
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

func generatorSource(ctrl *gomock.Controller, logger log.CtxLogger, nodeID string, batchSize, batchCount int) stream.Source {
	position := 0

	teardown := make(chan struct{})
	source := streammock.NewSource(ctrl)
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
			return nil, connectorPlugin.ErrStreamNotOpen
		}

		recs := make([]opencdc.Record, batchSize)
		for i := 0; i < batchSize; i++ {
			recs[i] = opencdc.Record{
				Position: opencdc.Position(strconv.Itoa(position)),
			}
			position++
		}

		return recs, nil
	}).MinTimes(batchCount + 1)
	source.EXPECT().Stop(gomock.Any()).DoAndReturn(func(context.Context) (opencdc.Position, error) {
		return opencdc.Position(strconv.Itoa(position)), nil
	})
	source.EXPECT().Errors().Return(make(chan error))

	return source
}

func printerDestination(ctrl *gomock.Controller, logger log.CtxLogger, nodeID string, batchSize int) stream.Destination {
	var lastPosition opencdc.Position
	_ = lastPosition
	rchan := make(chan opencdc.Record, batchSize)
	destination := streammock.NewDestination(ctrl)
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
	destination.EXPECT().Stop(gomock.Any(), gomock.Any()).Return(nil)
	destination.EXPECT().Teardown(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
		close(rchan)
		return nil
	})
	destination.EXPECT().Errors().Return(make(chan error))

	return destination
}

func noopDLQDestination(ctrl *gomock.Controller) stream.Destination {
	destination := streammock.NewDestination(ctrl)
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

func EqLazy(x func() interface{}) gomock.Matcher { return eqMatcherLazy{x} }

type eqMatcherLazy struct {
	x func() interface{}
}

func (e eqMatcherLazy) Matches(x interface{}) bool {
	return gomock.Eq(e.x()).Matches(x)
}

func (e eqMatcherLazy) String() string {
	return gomock.Eq(e.x()).String()
}
