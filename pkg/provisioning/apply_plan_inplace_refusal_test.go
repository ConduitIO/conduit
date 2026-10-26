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
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/lifecycle/starthook"
	"github.com/conduitio/conduit/pkg/pipeline"
	conn_plugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/conduitio/conduit/pkg/plugin/connector/connutils"
	"github.com/conduitio/conduit/pkg/plugin/connector/standalone"
	proc_plugin "github.com/conduitio/conduit/pkg/plugin/processor"
	proc_builtin "github.com/conduitio/conduit/pkg/plugin/processor/builtin"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

// TestApplyPlanLive_InPlaceRefusedByStartCheck_RollsBackStore: a live in-place
// processor change that the lifecycle refuses with the start check's code
// (pkg/lifecycle ReconfigureProcessor runs starthook before building) leaves
// the store on the old config. Real pipeline, connector and processor services
// on an in-memory DB; only the lifecycle is mocked. After the refused apply the
// stored processor has its old settings, a plan of the old config is empty,
// and the pipeline was never stopped or restarted (no StopAndWait or Start
// expectation: gomock fails the test if either is called).
func TestApplyPlanLive_InPlaceRefusedByStartCheck_RollsBackStore(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	logger := log.Nop()
	db := &inmemory.DB{}

	schemaReg, err := schemaregistry.NewSchemaRegistry(db)
	is.NoErr(err)
	tokens := connutils.NewAuthManager()
	connPlugins := conn_plugin.NewPluginService(
		logger,
		builtin.NewRegistry(logger, builtin.DefaultBuiltinConnectors, connutils.NewSchemaService(logger, schemaReg, tokens)),
		standalone.NewRegistry(logger, ""),
		tokens,
	)
	procPlugins := proc_plugin.NewPluginService(
		logger,
		proc_builtin.NewRegistry(logger, proc_builtin.DefaultBuiltinProcessors, schemaReg),
		nil,
	)

	plService := pipeline.NewService(logger, db)
	connService := connector.NewService(logger, db, connector.NewPersister(logger, db, time.Second, 3))
	procService := processor.NewService(logger, db, procPlugins)
	lifecycleSrv := mock.NewLifecycleService(ctrl)
	srv := NewService(db, logger, plService, connService, procService, connPlugins, lifecycleSrv, "")

	old := config.Pipeline{
		ID:     "p1",
		Status: config.StatusStopped,
		Name:   "p1",
		DLQ:    dlqFixture(),
		Connectors: []config.Connector{
			{ID: "p1:src", Name: "p1:src", Type: config.TypeSource, Plugin: "builtin:generator", Settings: map[string]string{"format.type": "raw", "format.options.foo": "bar"}},
			{ID: "p1:dst", Name: "p1:dst", Type: config.TypeDestination, Plugin: "builtin:log", Settings: map[string]string{}},
		},
		Processors: []config.Processor{
			{ID: "p1:proc", Plugin: "builtin:field.set", Settings: map[string]string{"field": ".Key", "value": "old"}, Workers: 1},
		},
	}
	is.NoErr(srv.Import(ctx, old))
	is.NoErr(plService.UpdateStatus(ctx, "p1", pipeline.StatusRunning, "")) // as if running

	desired := old
	desired.Processors = []config.Processor{
		{ID: "p1:proc", Plugin: "builtin:field.set", Settings: map[string]string{"field": ".Key", "value": "new"}, Workers: 1},
	}
	diff, err := srv.Plan(ctx, desired)
	is.NoErr(err)
	is.True(diff.LiveEligible()) // the change goes through the in-place swap

	refusal := conduiterr.New(starthook.CodeDebeziumStandaloneBeforeEncode, "can't apply processor change")
	lifecycleSrv.EXPECT().ReconfigureProcessor(gomock.Any(), "p1", "p1:proc").Return(refusal)

	_, err = srv.ApplyPlanLive(ctx, desired, diff.Hash, true)
	ce, ok := conduiterr.Get(err)
	is.True(ok) // the refusal reaches the caller with its code
	is.Equal(ce.Code, starthook.CodeDebeziumStandaloneBeforeEncode)

	got, err := procService.Get(ctx, "p1:proc")
	is.NoErr(err)
	is.Equal(got.Config.Settings["value"], "old") // store rolled back

	back, err := srv.Plan(ctx, old)
	is.NoErr(err)
	is.True(back.Empty()) // store equals the old config
}
