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

package builtin

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/hostpolicy"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/mock"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// A position embeds record key values for some connectors (a Postgres
// snapshot position holds the last primary key). The adapters log it as a
// size and a salted hash only, and the hash is the one the engine logs
// elsewhere, so a start and a later stop of one position can be matched.
func TestPluginAdapters_PositionsAreLoggedAsHashes(t *testing.T) {
	const secret = "customer-SECRET-4711"
	pos := opencdc.Position(`{"last_read":"` + secret + `"}`)

	t.Run("source Open", func(t *testing.T) {
		is := is.New(t)
		ctrl := gomock.NewController(t)
		src := mock.NewSourcePlugin(ctrl)
		src.EXPECT().Open(gomock.Any(), gomock.Any()).Return(pconnector.SourceOpenResponse{}, nil)

		var buf bytes.Buffer
		a := newSourcePluginAdapter(src, log.New(zerolog.New(&buf).Level(zerolog.DebugLevel)), hostpolicy.Policy{})
		_, err := a.Open(context.Background(), pconnector.SourceOpenRequest{Position: pos})
		is.NoErr(err)

		out := buf.String()
		is.True(strings.Contains(out, "calling Start"))
		is.True(strings.Contains(out, log.KeyHash(pos)))
		is.True(!strings.Contains(out, secret))
	})

	t.Run("destination Stop", func(t *testing.T) {
		is := is.New(t)
		ctrl := gomock.NewController(t)
		dst := mock.NewDestinationPlugin(ctrl)
		dst.EXPECT().Stop(gomock.Any(), gomock.Any()).Return(pconnector.DestinationStopResponse{}, nil)

		var buf bytes.Buffer
		a := newDestinationPluginAdapter(dst, log.New(zerolog.New(&buf).Level(zerolog.DebugLevel)), hostpolicy.Policy{})
		_, err := a.Stop(context.Background(), pconnector.DestinationStopRequest{LastPosition: pos})
		is.NoErr(err)

		out := buf.String()
		is.True(strings.Contains(out, "calling Stop"))
		is.True(strings.Contains(out, log.KeyHash(pos)))
		is.True(!strings.Contains(out, secret))
	})
}
