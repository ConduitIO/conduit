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

package provisioning

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func batchingApplyFixture() (old, desired config.Pipeline) {
	old = config.Pipeline{
		ID:  "p1",
		DLQ: dlqFixture(),
		Connectors: []config.Connector{
			{ID: "p1:src", Type: config.TypeSource, Plugin: "builtin:generator", Settings: map[string]string{"recordCount": "1"}},
			{ID: "p1:out", Type: config.TypeDestination, Plugin: "builtin:file", Settings: map[string]string{"path": "/tmp/x"}},
		},
	}
	desired = old
	desired.Connectors = []config.Connector{
		old.Connectors[0],
		{ID: "p1:out", Type: config.TypeDestination, Plugin: "builtin:file", Settings: map[string]string{"path": "/tmp/x", "sdk.batch.size": "100"}},
	}
	return old, desired
}

// TestApplyPlanLive_NoPreflightOnDefaultEngine pins that the default engine,
// which has no preflight, applies the same config through the normal
// stop-import-start sequence.
func TestApplyPlanLive_NoPreflightOnDefaultEngine(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	srv, pipSrv, connSrv, procSrv, _, lifecycleSrv := newTestService(ctrl, log.Nop())
	expectMark(t, lifecycleSrv, nil)

	old, desired := batchingApplyFixture()
	expectExportRunning(pipSrv, connSrv, procSrv, old)
	lifecycleSrv.EXPECT().StopAndWait(gomock.Any(), "p1").Return(nil)
	connSrv.EXPECT().Update(gomock.Any(), "p1:out", "builtin:file", gomock.Any()).Return(&connector.Instance{ID: "p1:out"}, nil)
	lifecycleSrv.EXPECT().Start(gomock.Any(), "p1").Return(nil)

	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, true)
	is.NoErr(err)
}
