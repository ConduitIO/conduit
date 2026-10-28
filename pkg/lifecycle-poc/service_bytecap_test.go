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

package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle-poc/funnel"
	"github.com/conduitio/conduit/pkg/pipeline"
	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// TestBuildDestinationTasks_ReceiveLimitOnlyForNonBuiltin pins the decision that
// connectors.max-receive-record-size caps writes to standalone destinations only.
// A built-in connector has no message limit; if the exemption regressed, or the
// cap were dropped for standalone connectors, a large batch would silently go
// back to failing at the plugin (or be split for no reason).
func TestBuildDestinationTasks_ReceiveLimitOnlyForNonBuiltin(t *testing.T) {
	const maxReceive = 4 << 20

	for name, tc := range map[string]struct {
		plugin    string
		wantLimit bool
	}{
		"builtin":    {plugin: "builtin:test-destination", wantLimit: false},
		"standalone": {plugin: "standalone:test-destination", wantLimit: true},
		// An unprefixed name may resolve to either; it is capped, which is safe.
		"unprefixed": {plugin: "test-destination", wantLimit: true},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			logger := log.New(zerolog.Nop())
			db := &inmemory.DB{}
			persister := connector.NewPersister(logger, db, time.Second, 3)
			ps := pipeline.NewService(logger, db)
			pl, err := ps.Create(ctx, uuid.NewString(), pipeline.Config{Name: "test pipeline"}, pipeline.ProvisionTypeAPI)
			is.NoErr(err)

			ctrl := gomock.NewController(t)
			destination := dummyDestination(persister)
			destination.Plugin = tc.plugin
			destDispenser := pmock.NewDispenser(ctrl)
			destDispenser.EXPECT().DispenseDestination().Return(pmock.NewDestinationPlugin(ctrl), nil).AnyTimes()
			pl, err = ps.AddConnector(ctx, pl.ID, destination.ID)
			is.NoErr(err)

			ls := NewService(
				logger,
				testErrRecoveryCfg(),
				testConnectorService{destination.ID: destination},
				newTestProcessorService(t, logger, db),
				testConnectorPluginService{tc.plugin: destDispenser},
				ps,
				false,
			)
			ls.SetMaxReceiveRecordSize(maxReceive)

			branches, err := ls.buildDestinationTasks(ctx, pl, logger)
			is.NoErr(err)
			is.Equal(len(branches), 1)
			dt, ok := branches[0][len(branches[0])-1].(*funnel.DestinationTask)
			is.True(ok)
			if tc.wantLimit {
				is.True(dt.WriteLimit() > 0)
				is.True(dt.WriteLimit() < maxReceive)
			} else {
				is.Equal(dt.WriteLimit(), 0)
			}
		})
	}
}
