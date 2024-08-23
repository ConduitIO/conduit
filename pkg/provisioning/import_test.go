// Copyright © 2023 Meroxa, Inc.
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

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/mock"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestService_ExecuteActions_Success(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)
	ctx := context.Background()

	srv, _, _, _, _ := newTestService(ctrl, logger)

	var actionCount int
	countingAction := fakeAction{do: func(ctx context.Context) error {
		actionCount++
		return nil
	}}

	actions := []action{countingAction, countingAction, countingAction}

	got, err := srv.executeActions(ctx, actions)
	is.NoErr(err)
	is.Equal(actionCount, 3)
	is.Equal(got, 3)
}

func TestService_ExecuteActions_Fail(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)
	ctx := context.Background()

	srv, _, _, _, _ := newTestService(ctrl, logger)

	var actionCount int
	countingAction := fakeAction{do: func(ctx context.Context) error {
		actionCount++
		return nil
	}}
	wantErr := cerrors.New("test error")
	failingAction := fakeAction{do: func(ctx context.Context) error {
		actionCount++
		return wantErr
	}}

	actions := []action{countingAction, countingAction, failingAction, countingAction}

	// executeActions stops executing actions when one fails
	got, err := srv.executeActions(ctx, actions)
	is.True(cerrors.Is(err, wantErr))
	is.Equal(actionCount, 3)
	is.Equal(got, 2) // expected the number of successfully executed actions
}

func TestService_RollbackActions_Success(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)
	ctx := context.Background()

	srv, _, _, _, _ := newTestService(ctrl, logger)

	var actionCount int
	countingAction := fakeAction{rollback: func(ctx context.Context) error {
		actionCount++
		return nil
	}}

	actions := []action{countingAction, countingAction, countingAction}

	ok := srv.rollbackActions(ctx, actions)
	is.True(ok)
	is.Equal(actionCount, 3)
}

func TestService_RollbackActions_Fail(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)
	ctx := context.Background()

	srv, _, _, _, _ := newTestService(ctrl, logger)

	var actionCount int
	countingAction := fakeAction{rollback: func(ctx context.Context) error {
		actionCount++
		return nil
	}}
	wantErr := cerrors.New("test error")
	failingAction := fakeAction{rollback: func(ctx context.Context) error {
		actionCount++
		return wantErr
	}}

	actions := []action{countingAction, countingAction, failingAction, countingAction}

	// rollbackActions executes all actions, regardless of the errors
	ok := srv.rollbackActions(ctx, actions)
	is.True(!ok)
	is.Equal(actionCount, 4) // expected all rollback actions to be executed
}

func TestActionBuilder_Build(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, pipSrv, connSrv, procSrv, connPlugSrv := newTestService(ctrl, logger)

	oldConfig := config.Pipeline{
		ID:   "config-id",
		Name: "old-name",
		Connectors: []config.Connector{{
			// this connector does not change, it should be ignored
			ID:   "conn-source-1",
			Type: config.TypeSource,
			Name: "conn-name-1",
		}, {
			// this connector contains an invalid change, it should be recreated
			ID:     "conn-source-2",
			Type:   config.TypeSource,
			Name:   "conn-name-2",
			Plugin: "old-plugin", // plugin was updated
		}, {
			// this connector gets a new name, it should be updated
			ID:   "conn-destination",
			Type: config.TypeDestination,
			Name: "conn-old-name",
		}, {
			// this connector is deleted in the new config, it should be deleted
			ID:   "conn-deleted-destination",
			Type: config.TypeDestination,
			Name: "conn-deleted-destination",
			Processors: []config.Processor{{
				// this processor is attached to a deleted connector, it should be deleted
				ID: "conn-proc",
			}},
		}},
		Processors: []config.Processor{{
			// this processor does not change, it should be ignored
			ID:     "proc-1",
			Plugin: "proc-type",
		}, {
			// this processor contains an invalid change, it should be recreated
			ID:     "proc-2",
			Plugin: "old-proc-type", // type was updated
		}, {
			// this processor gets new settings, it should be updated
			ID:       "proc-3",
			Plugin:   "proc-type",
			Settings: map[string]string{"foo": "bar"},
		}, {
			// this processor is deleted in the new config, it should be deleted
			ID:     "proc-deleted",
			Plugin: "proc-type",
		}},
	}
	newConfig := config.Pipeline{
		ID:   "config-id",
		Name: "old-name",
		Connectors: []config.Connector{{
			// this connector does not change, it should be ignored
			ID:   "conn-source-1",
			Type: config.TypeSource,
			Name: "conn-name-1",
		}, {
			// this connector contains an invalid change, it should be recreated
			ID:     "conn-source-2",
			Type:   config.TypeSource,
			Name:   "conn-name-2",
			Plugin: "new-plugin", // plugin was updated
		}, {
			// this connector gets a new name, it should be updated
			ID:   "conn-destination",
			Type: config.TypeDestination,
			Name: "conn-new-name",
		}, {
			// this connector is new, it should be created
			ID:   "conn-new-destination",
			Type: config.TypeDestination,
			Name: "conn-new-destination",
			Processors: []config.Processor{{
				// this processor is attached to a new connector, it should be created
				ID: "conn-proc",
			}},
		}},
		Processors: []config.Processor{{
			// this processor does not change, it should be ignored
			ID:     "proc-1",
			Plugin: "proc-type",
		}, {
			// this processor contains an invalid change, it should be recreated
			ID:     "proc-2",
			Plugin: "new-proc-type", // type was updated
		}, {
			// this processor gets new settings, it should be updated
			ID:       "proc-3",
			Plugin:   "proc-type",
			Settings: map[string]string{"foo": "baz"},
		}, {
			// this processor is new, it should be created
			ID:     "proc-new",
			Plugin: "proc-type",
		}},
	}

	wantOldActions := []action{
		deleteConnectorAction{
			cfg:                    oldConfig.Connectors[3],
			pipelineID:             oldConfig.ID,
			connectorService:       connSrv,
			connectorPluginService: connPlugSrv,
		},
		deleteProcessorAction{
			cfg: oldConfig.Connectors[3].Processors[0],
			parent: processor.Parent{
				ID:   oldConfig.Connectors[3].ID,
				Type: processor.ParentTypeConnector,
			},
			processorService: procSrv,
		},
		deleteProcessorAction{
			cfg: oldConfig.Processors[3],
			parent: processor.Parent{
				ID:   oldConfig.ID,
				Type: processor.ParentTypePipeline,
			},
			processorService: procSrv,
		},
	}
	wantNewActions := []action{
		updatePipelineAction{
			oldConfig:       oldConfig,
			newConfig:       newConfig,
			pipelineService: pipSrv,
		},
		deleteConnectorAction{
			cfg:                    oldConfig.Connectors[1],
			pipelineID:             oldConfig.ID,
			connectorService:       connSrv,
			connectorPluginService: connPlugSrv,
		},
		createConnectorAction{
			cfg:                    newConfig.Connectors[1],
			pipelineID:             newConfig.ID,
			connectorService:       connSrv,
			connectorPluginService: connPlugSrv,
		},
		updateConnectorAction{
			oldConfig:        oldConfig.Connectors[2],
			newConfig:        newConfig.Connectors[2],
			connectorService: connSrv,
		},
		createConnectorAction{
			cfg:                    newConfig.Connectors[3],
			pipelineID:             newConfig.ID,
			connectorService:       connSrv,
			connectorPluginService: connPlugSrv,
		},
		createProcessorAction{
			cfg: newConfig.Connectors[3].Processors[0],
			parent: processor.Parent{
				ID:   newConfig.Connectors[3].ID,
				Type: processor.ParentTypeConnector,
			},
			processorService: procSrv,
		},
		deleteProcessorAction{
			cfg: oldConfig.Processors[1],
			parent: processor.Parent{
				ID:   oldConfig.ID,
				Type: processor.ParentTypePipeline,
			},
			processorService: procSrv,
		},
		createProcessorAction{
			cfg: newConfig.Processors[1],
			parent: processor.Parent{
				ID:   newConfig.ID,
				Type: processor.ParentTypePipeline,
			},
			processorService: procSrv,
		},
		updateProcessorAction{
			oldConfig:        oldConfig.Processors[2],
			newConfig:        newConfig.Processors[2],
			processorService: procSrv,
		},
		createProcessorAction{
			cfg: newConfig.Processors[3],
			parent: processor.Parent{
				ID:   newConfig.ID,
				Type: processor.ParentTypePipeline,
			},
			processorService: procSrv,
		},
	}
	// we declare actions from top down to make it easier to read, expect
	// the actions to be returned in the reverse order though
	reverseActions(wantOldActions)

	t.Run("buildForOldConfig", func(t *testing.T) {
		is := is.New(t)

		got := srv.newActionsBuilder().buildForOldConfig(oldConfig, newConfig)
		is.Equal(got, wantOldActions)
	})
	t.Run("buildForNewConfig", func(t *testing.T) {
		is := is.New(t)

		got := srv.newActionsBuilder().buildForNewConfig(oldConfig, newConfig)
		is.Equal(got, wantNewActions)
	})
	t.Run("Build", func(t *testing.T) {
		is := is.New(t)

		got := srv.newActionsBuilder().Build(oldConfig, newConfig)
		var want []action
		want = append(want, wantOldActions...)
		want = append(want, wantNewActions...)
		is.Equal(got, want)
	})
}

func TestActionsBuilder_PreparePipelineActions_Create(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, pipSrv, _, _, _ := newTestService(ctrl, logger)

	oldConfig := config.Pipeline{}
	newConfig := config.Pipeline{ID: "config-id"}

	want := []action{createPipelineAction{
		cfg:             newConfig,
		pipelineService: pipSrv,
	}}

	got := srv.newActionsBuilder().preparePipelineActions(oldConfig, newConfig)
	is.Equal(got, want)
}

func TestActionsBuilder_PreparePipelineActions_Delete(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, pipSrv, _, _, _ := newTestService(ctrl, logger)

	oldConfig := config.Pipeline{ID: "config-id"}
	newConfig := config.Pipeline{}

	want := []action{deletePipelineAction{
		cfg:             oldConfig,
		pipelineService: pipSrv,
	}}

	got := srv.newActionsBuilder().preparePipelineActions(oldConfig, newConfig)
	is.Equal(got, want)
}

func TestActionsBuilder_PreparePipelineActions_NoAction(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, _, _ := newTestService(ctrl, logger)

	testCases := []struct {
		name      string
		oldConfig config.Pipeline
		newConfig config.Pipeline
	}{{
		name:      "empty",
		oldConfig: config.Pipeline{ID: "config-id"},
		newConfig: config.Pipeline{ID: "config-id"},
	}, {
		name:      "different Status",
		oldConfig: config.Pipeline{ID: "config-id", Status: "stopped"},
		newConfig: config.Pipeline{ID: "config-id", Status: "running"},
	}, {
		name: "different Connectors (same ID)",
		oldConfig: config.Pipeline{ID: "config-id", Connectors: []config.Connector{{
			ID:         "conn-id", // only ID has to match
			Type:       config.TypeSource,
			Plugin:     "old-plugin",
			Name:       "old-name",
			Settings:   map[string]string{"foo": "bar"},
			Processors: []config.Processor{{ID: "old-config-id"}},
		}}},
		newConfig: config.Pipeline{ID: "config-id", Connectors: []config.Connector{{
			ID:         "conn-id", // only ID has to match
			Type:       config.TypeDestination,
			Plugin:     "new-plugin",
			Name:       "new-name",
			Settings:   map[string]string{"foo": "baz"},
			Processors: []config.Processor{{ID: "new-config-id"}},
		}}},
	}, {
		name: "different Processors (same ID)",
		oldConfig: config.Pipeline{ID: "config-id", Processors: []config.Processor{{
			ID:       "proc-id", // only ID has to match
			Plugin:   "old-type",
			Settings: map[string]string{"foo": "bar"},
			Workers:  1,
		}}},
		newConfig: config.Pipeline{ID: "config-id", Processors: []config.Processor{{
			ID:       "proc-id", // only ID has to match
			Plugin:   "new-type",
			Settings: map[string]string{"foo": "baz"},
			Workers:  2,
		}}},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			got := srv.newActionsBuilder().preparePipelineActions(tc.oldConfig, tc.newConfig)
			is.Equal(got, nil) // did not expect any actions
		})
	}
}

func TestActionsBuilder_PreparePipelineActions_Update(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, pipSrv, _, _, _ := newTestService(ctrl, logger)

	testCases := []struct {
		name      string
		oldConfig config.Pipeline
		newConfig config.Pipeline
	}{{
		name:      "different Name",
		oldConfig: config.Pipeline{ID: "config-id", Name: "old-name"},
		newConfig: config.Pipeline{ID: "config-id", Name: "new-name"},
	}, {
		name:      "different Description",
		oldConfig: config.Pipeline{ID: "config-id", Description: "old-description"},
		newConfig: config.Pipeline{ID: "config-id", Description: "new-description"},
	}, {
		name:      "different Connectors.ID",
		oldConfig: config.Pipeline{ID: "config-id", Connectors: []config.Connector{{ID: "old-id"}}},
		newConfig: config.Pipeline{ID: "config-id", Connectors: []config.Connector{{ID: "new-id"}}},
	}, {
		name:      "different Processors.ID",
		oldConfig: config.Pipeline{ID: "config-id", Processors: []config.Processor{{ID: "old-id"}}},
		newConfig: config.Pipeline{ID: "config-id", Processors: []config.Processor{{ID: "new-id"}}},
	}, {
		name:      "different DLQ.Plugin",
		oldConfig: config.Pipeline{ID: "config-id", DLQ: config.DLQ{Plugin: "old-plugin"}},
		newConfig: config.Pipeline{ID: "config-id", DLQ: config.DLQ{Plugin: "new-plugin"}},
	}, {
		name:      "different DLQ.Settings",
		oldConfig: config.Pipeline{ID: "config-id", DLQ: config.DLQ{Settings: map[string]string{"foo": "bar"}}},
		newConfig: config.Pipeline{ID: "config-id", DLQ: config.DLQ{Settings: map[string]string{"foo": "baz"}}},
		// TODO different DLQ workers and threshold
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			want := []action{updatePipelineAction{
				oldConfig:       tc.oldConfig,
				newConfig:       tc.newConfig,
				pipelineService: pipSrv,
			}}
			got := srv.newActionsBuilder().preparePipelineActions(tc.oldConfig, tc.newConfig)
			is.Equal(got, want)
		})
	}
}

func TestActionsBuilder_PrepareConnectorActions_Create(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, connSrv, _, connPlugSrv := newTestService(ctrl, logger)

	oldConfig := config.Connector{}
	newConfig := config.Connector{ID: "config-id"}
	pipelineID := uuid.NewString()

	want := []action{createConnectorAction{
		cfg:                    newConfig,
		pipelineID:             pipelineID,
		connectorService:       connSrv,
		connectorPluginService: connPlugSrv,
	}}

	got := srv.newActionsBuilder().prepareConnectorActions(oldConfig, newConfig, pipelineID)
	is.Equal(got, want)
}

func TestActionsBuilder_PrepareConnectorActions_Delete(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, connSrv, _, connPlugSrv := newTestService(ctrl, logger)

	oldConfig := config.Connector{ID: "config-id"}
	newConfig := config.Connector{}
	pipelineID := uuid.NewString()

	want := []action{deleteConnectorAction{
		cfg:                    oldConfig,
		pipelineID:             pipelineID,
		connectorService:       connSrv,
		connectorPluginService: connPlugSrv,
	}}

	got := srv.newActionsBuilder().prepareConnectorActions(oldConfig, newConfig, pipelineID)
	is.Equal(got, want)
}

func TestActionsBuilder_PrepareConnectorActions_NoAction(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, _, _ := newTestService(ctrl, logger)

	testCases := []struct {
		name      string
		oldConfig config.Connector
		newConfig config.Connector
	}{{
		name:      "empty",
		oldConfig: config.Connector{ID: "config-id"},
		newConfig: config.Connector{ID: "config-id"},
	}, {
		name: "different Processors",
		oldConfig: config.Connector{ID: "config-id", Processors: []config.Processor{{
			ID:       "proc-id", // only ID has to match
			Plugin:   "old-type",
			Settings: map[string]string{"foo": "bar"},
			Workers:  1,
		}}},
		newConfig: config.Connector{ID: "config-id", Processors: []config.Processor{{
			ID:       "proc-id", // only ID has to match
			Plugin:   "new-type",
			Settings: map[string]string{"foo": "baz"},
			Workers:  2,
		}}},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			got := srv.newActionsBuilder().prepareConnectorActions(tc.oldConfig, tc.newConfig, uuid.NewString())
			is.Equal(got, nil) // did not expect any actions
		})
	}
}

func TestActionsBuilder_PrepareConnectorActions_Update(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, connSrv, _, _ := newTestService(ctrl, logger)

	testCases := []struct {
		name      string
		oldConfig config.Connector
		newConfig config.Connector
	}{{
		name:      "different Name",
		oldConfig: config.Connector{ID: "config-id", Name: "old-name"},
		newConfig: config.Connector{ID: "config-id", Name: "new-name"},
	}, {
		name:      "different Settings",
		oldConfig: config.Connector{ID: "config-id", Settings: map[string]string{"foo": "bar"}},
		newConfig: config.Connector{ID: "config-id", Settings: map[string]string{"foo": "baz"}},
	}, {
		name:      "different Processors.ID",
		oldConfig: config.Connector{ID: "config-id", Processors: []config.Processor{{ID: "old-id"}}},
		newConfig: config.Connector{ID: "config-id", Processors: []config.Processor{{ID: "new-id"}}},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			want := []action{updateConnectorAction{
				oldConfig:        tc.oldConfig,
				newConfig:        tc.newConfig,
				connectorService: connSrv,
			}}
			got := srv.newActionsBuilder().prepareConnectorActions(tc.oldConfig, tc.newConfig, uuid.NewString())
			is.Equal(got, want)
		})
	}
}

func TestActionsBuilder_PrepareConnectorActions_Recreate(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, connSrv, _, connPlugSrv := newTestService(ctrl, logger)
	pipelineID := uuid.NewString()

	testCases := []struct {
		name      string
		oldConfig config.Connector
		newConfig config.Connector
	}{{
		name:      "different Type",
		oldConfig: config.Connector{ID: "config-id", Type: config.TypeSource},
		newConfig: config.Connector{ID: "config-id", Type: config.TypeDestination},
	}, {
		name:      "different Plugin",
		oldConfig: config.Connector{ID: "config-id", Plugin: "old-plugin"},
		newConfig: config.Connector{ID: "config-id", Plugin: "new-plugin"},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			want := []action{deleteConnectorAction{
				cfg:                    tc.oldConfig,
				pipelineID:             pipelineID,
				connectorService:       connSrv,
				connectorPluginService: connPlugSrv,
			}, createConnectorAction{
				cfg:                    tc.newConfig,
				pipelineID:             pipelineID,
				connectorService:       connSrv,
				connectorPluginService: connPlugSrv,
			}}
			got := srv.newActionsBuilder().prepareConnectorActions(tc.oldConfig, tc.newConfig, pipelineID)
			is.Equal(got, want)
		})
	}
}

func TestActionsBuilder_PrepareProcessorActions_Create(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, procSrv, _ := newTestService(ctrl, logger)

	oldConfig := config.Processor{}
	newConfig := config.Processor{ID: "config-id"}
	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypePipeline,
	}

	want := []action{createProcessorAction{
		cfg:              newConfig,
		parent:           parent,
		processorService: procSrv,
	}}

	got := srv.newActionsBuilder().prepareProcessorActions(oldConfig, newConfig, parent)
	is.Equal(got, want)
}

func TestActionsBuilder_PrepareProcessorActions_Delete(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, procSrv, _ := newTestService(ctrl, logger)

	oldConfig := config.Processor{ID: "config-id"}
	newConfig := config.Processor{}
	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypePipeline,
	}

	want := []action{deleteProcessorAction{
		cfg:              oldConfig,
		parent:           parent,
		processorService: procSrv,
	}}

	got := srv.newActionsBuilder().prepareProcessorActions(oldConfig, newConfig, parent)
	is.Equal(got, want)
}

func TestActionsBuilder_PrepareProcessorActions_NoAction(t *testing.T) {
	is := is.New(t)
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, _, _ := newTestService(ctrl, logger)

	oldConfig := config.Processor{ID: "config-id"}
	newConfig := config.Processor{ID: "config-id"}
	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypeConnector,
	}

	got := srv.newActionsBuilder().prepareProcessorActions(oldConfig, newConfig, parent)
	is.Equal(got, nil) // did not expect any actions
}

func TestActionsBuilder_PrepareProcessorActions_Update(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, procSrv, _ := newTestService(ctrl, logger)
	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypeConnector,
	}

	testCases := []struct {
		name      string
		oldConfig config.Processor
		newConfig config.Processor
	}{{
		name:      "different Settings",
		oldConfig: config.Processor{ID: "config-id", Settings: map[string]string{"foo": "bar"}},
		newConfig: config.Processor{ID: "config-id", Settings: map[string]string{"foo": "baz"}},
	}, {
		name:      "different Workers",
		oldConfig: config.Processor{ID: "config-id", Workers: 1},
		newConfig: config.Processor{ID: "config-id", Workers: 2},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			want := []action{updateProcessorAction{
				oldConfig:        tc.oldConfig,
				newConfig:        tc.newConfig,
				processorService: procSrv,
			}}
			got := srv.newActionsBuilder().prepareProcessorActions(tc.oldConfig, tc.newConfig, parent)
			is.Equal(got, want)
		})
	}
}

func TestActionsBuilder_PrepareProcessorActions_Recreate(t *testing.T) {
	logger := log.Nop()
	ctrl := gomock.NewController(t)

	srv, _, _, procSrv, _ := newTestService(ctrl, logger)
	parent := processor.Parent{
		ID:   uuid.NewString(),
		Type: processor.ParentTypePipeline,
	}

	testCases := []struct {
		name      string
		oldConfig config.Processor
		newConfig config.Processor
	}{{
		name:      "different Type",
		oldConfig: config.Processor{ID: "config-id", Plugin: "old-type"},
		newConfig: config.Processor{ID: "config-id", Plugin: "new-type"},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			want := []action{deleteProcessorAction{
				cfg:              tc.oldConfig,
				parent:           parent,
				processorService: procSrv,
			}, createProcessorAction{
				cfg:              tc.newConfig,
				parent:           parent,
				processorService: procSrv,
			}}
			got := srv.newActionsBuilder().prepareProcessorActions(tc.oldConfig, tc.newConfig, parent)
			is.Equal(got, want)
		})
	}
}

// -------------
// -- HELPERS --
// -------------

func uint64Ptr(i uint64) *uint64 { return &i }

func newTestService(ctrl *gomock.Controller, logger log.CtxLogger) (*Service, *mock.PipelineService, *mock.ConnectorService, *mock.ProcessorService, *mock.ConnectorPluginService) {
	db := &inmemory.DB{}
	pipSrv := mock.NewPipelineService(ctrl)
	connSrv := mock.NewConnectorService(ctrl)
	procSrv := mock.NewProcessorService(ctrl)
	connPlugSrv := mock.NewConnectorPluginService(ctrl)

	srv := NewService(db, logger, pipSrv, connSrv, procSrv, connPlugSrv, "")

	return srv, pipSrv, connSrv, procSrv, connPlugSrv
}

type fakeAction struct {
	string   string
	do       func(context.Context) error
	rollback func(context.Context) error
}

func (f fakeAction) String() string                     { return f.string }
func (f fakeAction) Do(ctx context.Context) error       { return f.do(ctx) }
func (f fakeAction) Rollback(ctx context.Context) error { return f.rollback(ctx) }
