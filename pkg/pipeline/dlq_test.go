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

package pipeline

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	connmock "github.com/conduitio/conduit/pkg/connector/mock"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/matryer/is"
)

func TestDLQDestination_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	ctrl := gomock.NewController(t)
	connBuilder := connmock.Builder{Ctrl: ctrl}

	dest := connBuilder.NewDestinationMock("dlq-test", connector.Config{})
	dlq := &DLQDestination{
		Destination: dest,
		Logger:      logger,
	}

	want := record.Record{
		Position:  []byte("test-position"),
		Operation: record.OperationCreate,
		Metadata:  map[string]string{"foo": "bar"},
		Key:       record.RawData{Raw: []byte("key")},
		Payload: record.Change{
			Before: nil,
			After:  record.StructuredData{"baz": "qux"},
		},
	}

	// connHelper is used to ensure that Write and Ack are called simultaneously
	// otherwise we could experience a deadlock in the connector
	connHelper := make(chan struct{})
	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(context.Context) (record.Position, error) {
			<-connHelper // Write is happening same time as Ack
			return want.Position, nil
		})
	dest.EXPECT().Write(gomock.Any(), want).
		DoAndReturn(func(context.Context, record.Record) error {
			connHelper <- struct{}{} // signal to Ack that Write is happening
			return nil
		})

	err := dlq.Write(ctx, want)
	is.NoErr(err)
}

func TestDLQDestination_DestinationWriteError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	ctrl := gomock.NewController(t)
	connBuilder := connmock.Builder{Ctrl: ctrl}

	dest := connBuilder.NewDestinationMock("dlq-test", connector.Config{})
	dlq := &DLQDestination{
		Destination: dest,
		Logger:      logger,
	}

	rec := record.Record{
		Position: []byte("test-position"),
	}
	wantErr := cerrors.New("test write error")

	connHelper := make(chan struct{})
	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(ctx context.Context) (record.Position, error) {
			<-connHelper // Write is happening same time as Ack
			<-ctx.Done() // block until context is closed
			return nil, ctx.Err()
		})
	dest.EXPECT().Write(gomock.Any(), rec).
		DoAndReturn(func(context.Context, record.Record) error {
			connHelper <- struct{}{} // signal to Ack that Write is happening
			return wantErr
		})

	err := dlq.Write(ctx, rec)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
}

func TestDLQDestination_DestinationNack(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	ctrl := gomock.NewController(t)
	connBuilder := connmock.Builder{Ctrl: ctrl}

	dest := connBuilder.NewDestinationMock("dlq-test", connector.Config{})
	dlq := &DLQDestination{
		Destination: dest,
		Logger:      logger,
	}

	rec := record.Record{
		Position: []byte("test-position"),
	}
	wantErr := cerrors.New("test nack error")

	// connHelper is used to ensure that Write and Ack are called simultaneously
	// otherwise we could experience a deadlock in the connector
	connHelper := make(chan struct{})
	dest.EXPECT().Ack(gomock.Any()).
		DoAndReturn(func(context.Context) (record.Position, error) {
			<-connHelper                 // Write is happening same time as Ack
			return rec.Position, wantErr // record is nacked
		})
	dest.EXPECT().Write(gomock.Any(), rec).
		DoAndReturn(func(context.Context, record.Record) error {
			connHelper <- struct{}{} // signal to Ack that Write is happening
			return nil               // write function succeeded, error is sent back in nack
		})

	err := dlq.Write(ctx, rec)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
}
