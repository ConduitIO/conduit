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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// Import takes a pipeline config and imports it into Conduit.
func (s *Service) Import(ctx context.Context, newConfig config.Pipeline) error {
	oldConfig, err := s.Export(ctx, newConfig.ID)
	if err != nil && !cerrors.Is(err, pipeline.ErrInstanceNotFound) {
		return cerrors.Errorf("could not export pipeline with ID %v, this could mean the Conduit state is corrupted: %w", err)
	}

	actions := s.newActionsBuilder().Build(oldConfig, newConfig)

	failedActionIndex, err := s.executeActions(ctx, actions)
	if err != nil {
		rollbackActions := actions[:failedActionIndex+1]
		s.logger.Debug(ctx).Err(err).Msgf("rolling back %d import actions", len(rollbackActions))
		reverseActions(rollbackActions) // execute rollback actions in reversed order
		if ok := s.rollbackActions(ctx, rollbackActions); !ok {
			s.logger.Warn(ctx).Msg("some actions failed to be rolled back, Conduit state might be corrupted; please report this issue to the Conduit team")
		}
		return err
	}

	return nil
}

// executeActions executes the actions and returns the number of successfully
// executed actions and an error if an action failed. If an action fails the
// function returns immediately without executing any further actions.
func (s *Service) executeActions(ctx context.Context, actions []action) (int, error) {
	for i, a := range actions {
		s.logger.Debug(ctx).Str("action", a.String()).Msg("executing action")
		err := a.Do(ctx)
		if err != nil {
			return i, cerrors.Errorf("error executing action %q: %w", a.String(), err)
		}
	}
	return len(actions), nil
}

// rollbackActions rolls back all actions and logs errors encountered along the
// way without returning them (we can't recover from rollback errors). It only
// returns a boolean signaling if the rollback was successful or not.
func (s *Service) rollbackActions(ctx context.Context, actions []action) bool {
	ok := true
	for _, a := range actions {
		s.logger.Debug(ctx).Str("action", a.String()).Msg("rolling back action")
		err := a.Rollback(ctx)
		if err != nil {
			s.logger.Err(ctx, err).Str("action", a.String()).Msg("error rolling back action")
			ok = false
		}
	}
	return ok
}

func (s *Service) newActionsBuilder() actionsBuilder {
	return actionsBuilder{
		pipelineService:        s.pipelineService,
		connectorService:       s.connectorService,
		processorService:       s.processorService,
		connectorPluginService: s.connectorPluginService,
	}
}

type actionsBuilder struct {
	pipelineService        PipelineService
	connectorService       ConnectorService
	processorService       ProcessorService
	connectorPluginService ConnectorPluginService
}

func (ab actionsBuilder) Build(oldConfig, newConfig config.Pipeline) []action {
	var actions []action
	actions = append(actions, ab.buildForOldConfig(oldConfig, newConfig)...)
	actions = append(actions, ab.buildForNewConfig(oldConfig, newConfig)...)
	return actions
}

// buildForNewConfig builds actions needed for the new config to be provisioned.
// It either creates or updates existing entities.
func (ab actionsBuilder) buildForNewConfig(oldConfig, newConfig config.Pipeline) []action {
	var actions []action

	actions = append(actions, ab.preparePipelineActions(oldConfig, newConfig)...)

	for _, connNewConfig := range newConfig.Connectors {
		connOldConfig, _ := ab.findConnectorByID(oldConfig.Connectors, connNewConfig.ID)
		actions = append(actions, ab.prepareConnectorActions(connOldConfig, connNewConfig, newConfig.ID)...)

		parent := processor.Parent{
			ID:   connNewConfig.ID,
			Type: processor.ParentTypeConnector,
		}
		for _, procNewConfig := range connNewConfig.Processors {
			procOldConfig, _ := ab.findProcessorByID(connOldConfig.Processors, procNewConfig.ID)
			actions = append(actions, ab.prepareProcessorActions(procOldConfig, procNewConfig, parent)...)
		}
	}

	parent := processor.Parent{
		ID:   newConfig.ID,
		Type: processor.ParentTypePipeline,
	}
	for _, procNewConfig := range newConfig.Processors {
		procOldConfig, _ := ab.findProcessorByID(oldConfig.Processors, procNewConfig.ID)
		actions = append(actions, ab.prepareProcessorActions(procOldConfig, procNewConfig, parent)...)
	}
	return actions
}

// buildForOldConfig builds actions needed to delete existing entities from the
// old config that are not found in the new config.
func (ab actionsBuilder) buildForOldConfig(oldConfig, newConfig config.Pipeline) []action {
	var actions []action

	// skip creating pipeline actions, they are always prepared in buildForNewConfig

	for _, connOldConfig := range oldConfig.Connectors {
		connNewConfig, ok := ab.findConnectorByID(newConfig.Connectors, connOldConfig.ID)
		if !ok {
			// connector doesn't exist anymore, prepare delete action
			actions = append(actions, ab.prepareConnectorActions(connOldConfig, config.Connector{}, oldConfig.ID)...)
		}

		parent := processor.Parent{
			ID:   connOldConfig.ID,
			Type: processor.ParentTypeConnector,
		}
		for _, procOldConfig := range connOldConfig.Processors {
			_, ok := ab.findProcessorByID(connNewConfig.Processors, procOldConfig.ID)
			if !ok {
				// processor doesn't exist anymore, prepare delete action
				actions = append(actions, ab.prepareProcessorActions(procOldConfig, config.Processor{}, parent)...)
			}
		}
	}

	parent := processor.Parent{
		ID:   oldConfig.ID,
		Type: processor.ParentTypePipeline,
	}
	for _, procOldConfig := range oldConfig.Processors {
		_, ok := ab.findProcessorByID(newConfig.Processors, procOldConfig.ID)
		if !ok {
			// processor doesn't exist anymore, prepare delete action
			actions = append(actions, ab.prepareProcessorActions(procOldConfig, config.Processor{}, parent)...)
		}
	}

	// reverse actions so that we first delete the innermost entities
	reverseActions(actions)
	return actions
}

func (ab actionsBuilder) findConnectorByID(connectors []config.Connector, id string) (config.Connector, bool) {
	for _, conn := range connectors {
		if conn.ID == id {
			return conn, true
		}
	}
	return config.Connector{}, false
}

func (ab actionsBuilder) findProcessorByID(processors []config.Processor, id string) (config.Processor, bool) {
	for _, proc := range processors {
		if proc.ID == id {
			return proc, true
		}
	}
	return config.Processor{}, false
}

func (ab actionsBuilder) preparePipelineActions(oldConfig, newConfig config.Pipeline) []action {
	if oldConfig.ID == "" {
		// no old config, it's a brand new pipeline
		return []action{createPipelineAction{
			cfg:             newConfig,
			pipelineService: ab.pipelineService,
		}}
	} else if newConfig.ID == "" {
		// no new config, it's an old pipeline that needs to be deleted
		return []action{deletePipelineAction{
			cfg:             oldConfig,
			pipelineService: ab.pipelineService,
		}}
	}

	// compare configs but ignore nested configs and some fields
	opts := []cmp.Option{
		cmpopts.IgnoreFields(config.Pipeline{}, config.PipelineIgnoredFields...),
		cmp.Comparer(func(c1, c2 config.Connector) bool { return c1.ID == c2.ID }),
		cmp.Comparer(func(c1, c2 config.Processor) bool { return c1.ID == c2.ID }),
	}

	if !cmp.Equal(oldConfig, newConfig, opts...) {
		return []action{updatePipelineAction{
			oldConfig:       oldConfig,
			newConfig:       newConfig,
			pipelineService: ab.pipelineService,
		}}
	}

	return nil
}

func (ab actionsBuilder) prepareConnectorActions(oldConfig, newConfig config.Connector, pipelineID string) []action {
	if oldConfig.ID == "" {
		// no old config, it's a brand new connector
		return []action{createConnectorAction{
			cfg:                    newConfig,
			pipelineID:             pipelineID,
			connectorService:       ab.connectorService,
			connectorPluginService: ab.connectorPluginService,
		}}
	} else if newConfig.ID == "" {
		// no new config, it's an old connector that needs to be deleted
		return []action{deleteConnectorAction{
			cfg:                    oldConfig,
			pipelineID:             pipelineID,
			connectorService:       ab.connectorService,
			connectorPluginService: ab.connectorPluginService,
		}}
	}

	// first compare configs but ignore nested configs
	opts := []cmp.Option{
		cmp.Comparer(func(p1, p2 config.Processor) bool { return p1.ID == p2.ID }),
	}
	if cmp.Equal(oldConfig, newConfig, opts...) {
		// configs match, no need to do anything
		return nil
	}

	// compare them again but ignore mutable fields, if configs are still
	// different an update is not possible, we have to entirely recreate the
	// connector
	opts = []cmp.Option{
		cmpopts.IgnoreFields(config.Connector{}, config.ConnectorMutableFields...),
	}
	if cmp.Equal(oldConfig, newConfig, opts...) {
		// only updatable fields don't match, we can update the connector
		return []action{updateConnectorAction{
			oldConfig:        oldConfig,
			newConfig:        newConfig,
			connectorService: ab.connectorService,
		}}
	}

	// we have to delete the old connector and create a new one
	return []action{
		deleteConnectorAction{
			cfg:                    oldConfig,
			pipelineID:             pipelineID,
			connectorService:       ab.connectorService,
			connectorPluginService: ab.connectorPluginService,
		},
		createConnectorAction{
			cfg:                    newConfig,
			pipelineID:             pipelineID,
			connectorService:       ab.connectorService,
			connectorPluginService: ab.connectorPluginService,
		},
	}
}

func (ab actionsBuilder) prepareProcessorActions(oldConfig, newConfig config.Processor, parent processor.Parent) []action {
	if oldConfig.ID == "" {
		// no old config, it's a brand-new processor
		return []action{createProcessorAction{
			cfg:              newConfig,
			parent:           parent,
			processorService: ab.processorService,
		}}
	} else if newConfig.ID == "" {
		// no new config, it's an old processor that needs to be deleted
		return []action{deleteProcessorAction{
			cfg:              oldConfig,
			parent:           parent,
			processorService: ab.processorService,
		}}
	}

	// configs match, no need to do anything
	if cmp.Equal(oldConfig, newConfig) {
		return nil
	}

	// the processor changed, and all parts of a processor are updateable
	return []action{updateProcessorAction{
		oldConfig:        oldConfig,
		newConfig:        newConfig,
		processorService: ab.processorService,
	}}
}

func reverseActions(actions []action) {
	for i, j := 0, len(actions)-1; i < j; i, j = i+1, j-1 {
		actions[i], actions[j] = actions[j], actions[i]
	}
}
