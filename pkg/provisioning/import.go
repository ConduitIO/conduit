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
		s.logger.Warn(ctx).Err(err).Msgf("could not export pipeline with ID %v, trying to provision new pipeline despite the error", newConfig.ID)
		// TODO does this behavior make sense? Should we rather return the error
		//  and skip provisioning?
	}

	var actions []action
	actions = append(actions, s.cascadingActionsDeleteOld(oldConfig, newConfig)...)
	actions = append(actions, s.cascadingActionsUpsertNew(oldConfig, newConfig)...)

	failedActionIndex, err := s.executeActions(ctx, actions)
	if err != nil {
		rollbackActions := actions[:failedActionIndex+1]
		s.logger.Debug(ctx).Err(err).Msgf("rolling back %d import actions", len(rollbackActions))
		s.reverseActions(rollbackActions) // execute rollback actions in reversed order
		if ok := s.rollbackActions(ctx, rollbackActions); !ok {
			s.logger.Warn(ctx).Msg("some actions failed to be rolled back, Conduit state might be corrupted; please report this issue to the Conduit team")
		}
		return err
	}

	return nil
}

// cascadingActionsUpsertNew prepares actions needed for the new config to be
// provisioned. It either creates or updates existing entities.
func (s *Service) cascadingActionsUpsertNew(oldConfig, newConfig config.Pipeline) []action {
	var actions []action

	actions = append(actions, s.preparePipelineActions(oldConfig, newConfig)...)

	for _, connNewConfig := range newConfig.Connectors {
		connOldConfig, _ := s.findConnectorByID(oldConfig.Connectors, connNewConfig.ID)
		actions = append(actions, s.prepareConnectorActions(connOldConfig, connNewConfig, newConfig.ID)...)

		parent := processor.Parent{
			ID:   connNewConfig.ID,
			Type: processor.ParentTypeConnector,
		}
		for _, procNewConfig := range connNewConfig.Processors {
			procOldConfig, _ := s.findProcessorByID(connOldConfig.Processors, procNewConfig.ID)
			actions = append(actions, s.prepareProcessorActions(procOldConfig, procNewConfig, parent)...)
		}
	}

	parent := processor.Parent{
		ID:   newConfig.ID,
		Type: processor.ParentTypePipeline,
	}
	for _, procNewConfig := range newConfig.Processors {
		procOldConfig, _ := s.findProcessorByID(oldConfig.Processors, procNewConfig.ID)
		actions = append(actions, s.prepareProcessorActions(procOldConfig, procNewConfig, parent)...)
	}
	return actions
}

// cascadingActionsDeleteOld prepares actions needed to delete existing entities
// from the old config that are not found in the new config.
func (s *Service) cascadingActionsDeleteOld(oldConfig, newConfig config.Pipeline) []action {
	var actions []action

	if newConfig.ID == "" {
		// pipeline doesn't exist anymore, prepare delete action
		actions = append(actions, s.preparePipelineActions(oldConfig, config.Pipeline{})...)
	}

	for _, connOldConfig := range oldConfig.Connectors {
		connNewConfig, ok := s.findConnectorByID(newConfig.Connectors, connOldConfig.ID)
		if !ok {
			// connector doesn't exist anymore, prepare delete action
			actions = append(actions, s.prepareConnectorActions(connOldConfig, config.Connector{}, oldConfig.ID)...)
		}

		parent := processor.Parent{
			ID:   connOldConfig.ID,
			Type: processor.ParentTypeConnector,
		}
		for _, procOldConfig := range connOldConfig.Processors {
			_, ok := s.findProcessorByID(connNewConfig.Processors, procOldConfig.ID)
			if !ok {
				// processor doesn't exist anymore, prepare delete action
				actions = append(actions, s.prepareProcessorActions(procOldConfig, config.Processor{}, parent)...)
			}
		}
	}

	parent := processor.Parent{
		ID:   oldConfig.ID,
		Type: processor.ParentTypePipeline,
	}
	for _, procOldConfig := range oldConfig.Processors {
		_, ok := s.findProcessorByID(newConfig.Processors, procOldConfig.ID)
		if !ok {
			// processor doesn't exist anymore, prepare delete action
			actions = append(actions, s.prepareProcessorActions(procOldConfig, config.Processor{}, parent)...)
		}
	}

	// reverse actions so that we first delete the innermost entities
	s.reverseActions(actions)
	return actions
}

func (s *Service) findConnectorByID(connectors []config.Connector, id string) (config.Connector, bool) {
	for _, conn := range connectors {
		if conn.ID == id {
			return conn, true
		}
	}
	return config.Connector{}, false
}

func (s *Service) findProcessorByID(processors []config.Processor, id string) (config.Processor, bool) {
	for _, proc := range processors {
		if proc.ID == id {
			return proc, true
		}
	}
	return config.Processor{}, false
}

// executeActions executes the actions and returns the number of successfully
// executed actions and an error if an action failed. If an action fails the
// function returns immediately without executing any further actions.
func (s *Service) executeActions(ctx context.Context, actions []action) (int, error) {
	for i, a := range actions {
		s.logger.Trace(ctx).Str("action", a.String()).Msg("executing action")
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
		s.logger.Trace(ctx).Str("action", a.String()).Msg("rolling back action")
		err := a.Rollback(ctx)
		if err != nil {
			s.logger.Err(ctx, err).Str("action", a.String()).Msg("error rolling back action")
			ok = false
		}
	}
	return ok
}

func (s *Service) preparePipelineActions(oldConfig, newConfig config.Pipeline) []action {
	if oldConfig.ID == "" {
		// no old config, it's a brand new pipeline
		return []action{createPipelineAction{
			cfg:             newConfig,
			pipelineService: s.pipelineService,
		}}
	} else if newConfig.ID == "" {
		// no new config, it's an old pipeline that needs to be deleted
		return []action{deletePipelineAction{
			cfg:             oldConfig,
			pipelineService: s.pipelineService,
		}}
	}

	// compare configs but ignore nested configs and some fields
	opts := []cmp.Option{
		cmpopts.IgnoreFields(config.Pipeline{}, "Status"),
		cmp.Comparer(func(c1, c2 config.Connector) bool { return c1.ID == c2.ID }),
		cmp.Comparer(func(c1, c2 config.Processor) bool { return c1.ID == c2.ID }),
	}

	if !cmp.Equal(oldConfig, newConfig, opts...) {
		return []action{updatePipelineAction{
			oldConfig:       oldConfig,
			newConfig:       newConfig,
			pipelineService: s.pipelineService,
		}}
	}

	return nil
}

func (s *Service) prepareConnectorActions(oldConfig, newConfig config.Connector, pipelineID string) []action {
	if oldConfig.ID == "" {
		// no old config, it's a brand new connector
		return []action{createConnectorAction{
			cfg:              newConfig,
			pipelineID:       pipelineID,
			connectorService: s.connectorService,
			pluginService:    s.pluginService,
		}}
	} else if newConfig.ID == "" {
		// no new config, it's an old connector that needs to be deleted
		return []action{deleteConnectorAction{
			cfg:              oldConfig,
			pipelineID:       pipelineID,
			connectorService: s.connectorService,
			pluginService:    s.pluginService,
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

	// compare them again but ignore fields that can be updated, if configs
	// are still different an update is not possible, we have to entirely
	// recreate the connector
	opts = []cmp.Option{
		cmpopts.IgnoreFields(config.Connector{}, "Name", "Settings", "Processors"),
	}
	if cmp.Equal(oldConfig, newConfig, opts...) {
		// only updatable fields don't match, we can update the connector
		return []action{updateConnectorAction{
			oldConfig:        oldConfig,
			newConfig:        newConfig,
			connectorService: s.connectorService,
		}}
	}

	// we have to delete the old connector and create a new one
	return []action{
		deleteConnectorAction{
			cfg:              oldConfig,
			pipelineID:       pipelineID,
			connectorService: s.connectorService,
			pluginService:    s.pluginService,
		},
		createConnectorAction{
			cfg:              newConfig,
			pipelineID:       pipelineID,
			connectorService: s.connectorService,
			pluginService:    s.pluginService,
		},
	}
}

func (s *Service) prepareProcessorActions(oldConfig, newConfig config.Processor, parent processor.Parent) []action {
	if oldConfig.ID == "" {
		// no old config, it's a brand new processor
		return []action{createProcessorAction{
			cfg:              newConfig,
			parent:           parent,
			processorService: s.processorService,
		}}
	} else if newConfig.ID == "" {
		// no new config, it's an old processor that needs to be deleted
		return []action{deleteProcessorAction{
			cfg:              oldConfig,
			parent:           parent,
			processorService: s.processorService,
		}}
	}

	// first compare whole configs
	if cmp.Equal(oldConfig, newConfig) {
		// configs match, no need to do anything
		return nil
	}

	// compare them again but ignore fields that can be updated, if configs
	// are still different an update is not possible, we have to entirely
	// recreate the processor
	opts := []cmp.Option{
		cmpopts.IgnoreFields(config.Processor{}, "Settings", "Workers"),
	}
	if cmp.Equal(oldConfig, newConfig, opts...) {
		// only updatable fields don't match, we can update the processor
		return []action{updateProcessorAction{
			oldConfig:        oldConfig,
			newConfig:        newConfig,
			processorService: s.processorService,
		}}
	}

	// we have to delete the old processor and create a new one
	return []action{
		deleteProcessorAction{
			cfg:              oldConfig,
			parent:           parent,
			processorService: s.processorService,
		},
		createProcessorAction{
			cfg:              newConfig,
			parent:           parent,
			processorService: s.processorService,
		},
	}
}

func (*Service) reverseActions(actions []action) {
	for i, j := 0, len(actions)-1; i < j; i, j = i+1, j-1 {
		actions[i], actions[j] = actions[j], actions[i]
	}
}
