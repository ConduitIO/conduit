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

package connector

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// A position embeds record key values for some connectors, so the stop log
// carries a salted hash of it, never the position.
func TestDestination_StopLogsPositionHashOnly(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dest, destinationMock := newTestDestination(ctx, t, ctrl)
	dest.Instance.LastActiveConfig = dest.Instance.Config // no lifecycle event
	_ = expectDestinationOpen(dest, destinationMock)
	is.NoErr(dest.Open(ctx))

	var buf bytes.Buffer
	dest.Instance.logger = log.New(zerolog.New(&buf).Level(zerolog.DebugLevel))

	const secret = "customer-SECRET-4711"
	pos := opencdc.Position(`{"last_read":"` + secret + `"}`)
	destinationMock.EXPECT().Stop(gomock.Any(), pconnector.DestinationStopRequest{LastPosition: pos}).
		Return(pconnector.DestinationStopResponse{}, nil)
	is.NoErr(dest.Stop(ctx, pos))

	out := buf.String()
	is.True(strings.Contains(out, "sending stop signal"))
	is.True(strings.Contains(out, log.KeyHash(pos)))
	is.True(!strings.Contains(out, secret))
}

// Same for the source: the stop response position is hashed, never logged.
func TestSource_StopLogsPositionHashOnly(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)
	src.Instance.LastActiveConfig = src.Instance.Config // no lifecycle event
	_ = expectSourceOpen(src, sourceMock)
	is.NoErr(src.Open(ctx))

	var buf bytes.Buffer
	src.Instance.logger = log.New(zerolog.New(&buf).Level(zerolog.DebugLevel))

	const secret = "customer-SECRET-4711"
	pos := opencdc.Position(`{"last_read":"` + secret + `"}`)
	sourceMock.EXPECT().Stop(gomock.Any(), pconnector.SourceStopRequest{}).
		Return(pconnector.SourceStopResponse{LastPosition: pos}, nil)
	got, err := src.Stop(ctx)
	is.NoErr(err)
	is.Equal(got, pos) // the real position is still returned to the caller

	out := buf.String()
	is.True(strings.Contains(out, "successfully responded to stop signal"))
	is.True(strings.Contains(out, log.KeyHash(pos)))
	is.True(!strings.Contains(out, secret))
}
