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

package conduit

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	dbmock "github.com/conduitio/conduit-commons/database/mock"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestRuntime_readiness_Starting(t *testing.T) {
	is := is.New(t)
	// Ready is not closed -> still starting up.
	r := &Runtime{Ready: make(chan struct{})}

	resp, code := r.readiness(context.Background())
	is.Equal(code, http.StatusServiceUnavailable)
	is.Equal(resp.Status, readinessStarting)
	is.Equal(resp.Pipelines, nil)
}

func TestRuntime_readiness_StoreUnavailable(t *testing.T) {
	is := is.New(t)
	ctrl := gomock.NewController(t)

	db := dbmock.NewDB(ctrl)
	db.EXPECT().Ping(gomock.Any()).Return(cerrors.New("connection refused"))

	ready := make(chan struct{})
	close(ready)
	r := &Runtime{Ready: ready, DB: db}

	resp, code := r.readiness(context.Background())
	is.Equal(code, http.StatusServiceUnavailable)
	is.Equal(resp.Status, readinessUnavailable)
	is.True(strings.Contains(resp.Reason, "connection refused"))
}

func TestRuntime_readiness_Ready(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	ps := pipeline.NewService(log.Nop(), db)

	running, err := ps.Create(ctx, "p-running", pipeline.Config{Name: "running"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)
	running.SetStatus(pipeline.StatusRunning)

	degraded, err := ps.Create(ctx, "p-degraded", pipeline.Config{Name: "degraded"}, pipeline.ProvisionTypeAPI)
	is.NoErr(err)
	degraded.SetStatus(pipeline.StatusDegraded)
	degraded.Error = "source connector error"

	ready := make(chan struct{})
	close(ready)
	r := &Runtime{Ready: ready, DB: db, pipelineService: ps}

	resp, code := r.readiness(ctx)
	// Degraded pipelines do NOT make the engine not-ready.
	is.Equal(code, http.StatusOK)
	is.Equal(resp.Status, readinessReady)
	is.True(resp.Pipelines != nil)
	is.Equal(resp.Pipelines.Total, 2)
	is.Equal(resp.Pipelines.Running, 1)
	is.Equal(resp.Pipelines.Degraded, 1)
	is.Equal(len(resp.Pipelines.Detail), 1)
	is.Equal(resp.Pipelines.Detail[0].ID, "p-degraded")
	is.Equal(resp.Pipelines.Detail[0].Error, "source connector error")
}
