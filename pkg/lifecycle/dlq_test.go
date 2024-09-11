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

package lifecycle

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	streammock "github.com/conduitio/conduit/pkg/lifecycle/stream/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestDLQDestination_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	ctrl := gomock.NewController(t)
	dest := streammock.NewDestination(ctrl)

	dlq := &DLQDestination{
		Destination: dest,
		Logger:      logger,
	}

	want := opencdc.Record{
		Position:  []byte("test-position"),
		Operation: opencdc.OperationCreate,
		Metadata:  map[string]string{"foo": "bar"},
		Key:       opencdc.RawData("key"),
		Payload: opencdc.Change{
			Before: nil,
			After:  opencdc.StructuredData{"baz": "qux"},
		},
	}

	dest.EXPECT().Write(gomock.Any(), []opencdc.Record{want}).Return(nil)
	dest.EXPECT().Ack(gomock.Any()).Return([]connector.DestinationAck{{Position: want.Position}}, nil)

	err := dlq.Write(ctx, want)
	is.NoErr(err)
}

func TestDLQDestination_DestinationWriteError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	ctrl := gomock.NewController(t)
	dest := streammock.NewDestination(ctrl)

	dlq := &DLQDestination{
		Destination: dest,
		Logger:      logger,
	}

	rec := opencdc.Record{
		Position: []byte("test-position"),
	}
	wantErr := cerrors.New("test write error")

	dest.EXPECT().Write(gomock.Any(), []opencdc.Record{rec}).Return(wantErr)

	err := dlq.Write(ctx, rec)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
}

func TestDLQDestination_DestinationNack(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()
	ctrl := gomock.NewController(t)
	dest := streammock.NewDestination(ctrl)

	dlq := &DLQDestination{
		Destination: dest,
		Logger:      logger,
	}

	rec := opencdc.Record{
		Position: []byte("test-position"),
	}
	wantErr := cerrors.New("test nack error")

	dest.EXPECT().Write(gomock.Any(), []opencdc.Record{rec}).Return(nil)
	dest.EXPECT().Ack(gomock.Any()).Return([]connector.DestinationAck{{Position: rec.Position, Error: wantErr}}, nil)

	err := dlq.Write(ctx, rec)
	is.True(err != nil)
	is.True(cerrors.Is(err, wantErr))
}
