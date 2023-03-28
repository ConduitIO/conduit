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
	"fmt"

	"github.com/conduitio/conduit/pkg/connector"
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
	actions = append(actions, s.prepareActionsOldConfig(oldConfig, newConfig)...)
	actions = append(actions, s.prepareActionsNewConfig(oldConfig, newConfig)...)

	lastActionIndex, err := s.executeActions(ctx, actions)
	if err != nil {
		s.logger.Debug(ctx).Err(err).Msgf("rolling back %d import actions", lastActionIndex+1)
		if ok := s.rollbackActions(ctx, actions[:lastActionIndex+1]); !ok {
			s.logger.Warn(ctx).Msg("some actions failed to be rolled back, Conduit state might be corrupted; please report this issue to the Conduit team")
		}
		return err
	}

	return nil
}

// prepareActionsNewConfig prepares actions needed for the new config to be
// provisioned. It either creates or updates existing entities.
func (s *Service) prepareActionsNewConfig(oldConfig, newConfig config.Pipeline) []action {
	var actions []action

	actions = append(actions, s.preparePipelineActions(oldConfig, newConfig)...)

	for _, connNewConfig := range newConfig.Connectors {
		var connOldConfig config.Connector
		for _, conn := range oldConfig.Connectors {
			if conn.ID == connNewConfig.ID {
				connOldConfig = conn
				break
			}
		}
		actions = append(actions, s.prepareConnectorActions(connOldConfig, connNewConfig, newConfig.ID)...)

		parent := processor.Parent{
			ID:   connNewConfig.ID,
			Type: processor.ParentTypeConnector,
		}
		for _, procNewConfig := range connNewConfig.Processors {
			var procOldConfig config.Processor
			for _, proc := range connOldConfig.Processors {
				if proc.ID == procNewConfig.ID {
					procOldConfig = proc
					break
				}
			}
			actions = append(actions, s.prepareProcessorActions(procOldConfig, procNewConfig, parent)...)
		}
	}

	parent := processor.Parent{
		ID:   newConfig.ID,
		Type: processor.ParentTypePipeline,
	}
	for _, procNewConfig := range newConfig.Processors {
		var procOldConfig config.Processor
		for _, proc := range oldConfig.Processors {
			if proc.ID == procNewConfig.ID {
				procOldConfig = proc
				break
			}
		}
		actions = append(actions, s.prepareProcessorActions(procOldConfig, procNewConfig, parent)...)
	}
	return actions
}

// prepareActionsOldConfig prepares actions needed to delete existing entities
// from the old config that are not found in the new config.
func (s *Service) prepareActionsOldConfig(oldConfig, newConfig config.Pipeline) []action {
	var actions []action

	if newConfig.ID == "" {
		// pipeline doesn't exist anymore, prepare delete action
		actions = append(actions, s.preparePipelineActions(oldConfig, config.Pipeline{})...)
	}

	for _, connOldConfig := range oldConfig.Connectors {
		var connNewConfig config.Connector
		for _, conn := range newConfig.Connectors {
			if connOldConfig.ID == conn.ID {
				connNewConfig = conn
				break
			}
		}

		if connNewConfig.ID == "" {
			// connector doesn't exist anymore, prepare delete action
			actions = append(actions, s.prepareConnectorActions(connOldConfig, config.Connector{}, oldConfig.ID)...)
		}

		parent := processor.Parent{
			ID:   connOldConfig.ID,
			Type: processor.ParentTypeConnector,
		}
		for _, procOldConfig := range connOldConfig.Processors {
			var procExists bool
			for _, proc := range connNewConfig.Processors {
				if proc.ID == procOldConfig.ID {
					procExists = true
					break
				}
			}
			if !procExists {
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
		var procExists bool
		for _, proc := range newConfig.Processors {
			if proc.ID == procOldConfig.ID {
				procExists = true
				break
			}
		}
		if !procExists {
			// processor doesn't exist anymore, prepare delete action
			actions = append(actions, s.prepareProcessorActions(procOldConfig, config.Processor{}, parent)...)
		}
	}

	// reverse actions so that we first delete the innermost entities
	for i, j := 0, len(actions)-1; i < j; i, j = i+1, j-1 {
		actions[i], actions[j] = actions[j], actions[i]
	}
	return actions
}

// executeActions executes the actions and returns the number of successfully
// executed actions and an error if an action failed. If an action fails the
// function returns immediately without executing any further actions.
func (s *Service) executeActions(ctx context.Context, actions []action) (int, error) {
	for i, a := range actions {
		s.logger.Trace(ctx).Str("action", a.String()).Msg("executing import action")
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
		s.logger.Trace(ctx).Str("action", a.String()).Msg("rolling back import action")
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

type action interface {
	String() string
	Do(context.Context) error
	Rollback(context.Context) error
}

// --------------------
// -- CREATE ACTIONS --
// --------------------

type createPipelineAction struct {
	cfg config.Pipeline

	pipelineService PipelineService
}

func (a createPipelineAction) String() string {
	return fmt.Sprintf("create pipeline with ID %v", a.cfg.ID)
}
func (a createPipelineAction) Do(ctx context.Context) error {
	_, err := a.pipelineService.Create(ctx, a.cfg.ID, pipeline.Config{
		Name:        a.cfg.Name,
		Description: a.cfg.Description,
	}, pipeline.ProvisionTypeConfig)
	if err != nil {
		return cerrors.Errorf("failed to create pipeline: %w", err)
	}
	_, err = a.pipelineService.UpdateDLQ(ctx, a.cfg.ID, pipeline.DLQ{
		Plugin:              a.cfg.DLQ.Plugin,               // TODO default
		Settings:            a.cfg.DLQ.Settings,             // TODO default
		WindowSize:          *a.cfg.DLQ.WindowSize,          // TODO nil pointer
		WindowNackThreshold: *a.cfg.DLQ.WindowNackThreshold, // TODO nil pointer
	})
	if err != nil {
		return cerrors.Errorf("failed to update pipeline DLQ: %w", err)
	}

	// add connector IDs
	for _, conn := range a.cfg.Connectors {
		_, err = a.pipelineService.AddConnector(ctx, a.cfg.ID, conn.ID)
		if err != nil {
			return cerrors.Errorf("failed to add connector %v: %w", conn.ID, err)
		}
	}
	// add processor IDs
	for _, proc := range a.cfg.Processors {
		_, err = a.pipelineService.AddProcessor(ctx, a.cfg.ID, proc.ID)
		if err != nil {
			return cerrors.Errorf("failed to add processor %v: %w", proc.ID, err)
		}
	}

	return nil
}
func (a createPipelineAction) Rollback(ctx context.Context) error {
	err := a.pipelineService.Delete(ctx, a.cfg.ID)
	if !cerrors.Is(err, pipeline.ErrInstanceNotFound) {
		// ignore instance not found errors, this means the action failed to
		// create the pipeline in the first place
		return err
	}
	return nil
}

type createConnectorAction struct {
	cfg        config.Connector
	pipelineID string

	connectorService ConnectorService
	pluginService    PluginService
}

func (a createConnectorAction) String() string {
	return fmt.Sprintf("create connector with ID %v", a.cfg.ID)
}
func (a createConnectorAction) Do(ctx context.Context) error {
	_, err := a.connectorService.Create(
		ctx,
		a.cfg.ID,
		a.connectorType(a.cfg.Type),
		a.cfg.Plugin,
		a.pipelineID,
		connector.Config{
			Name:     a.cfg.Name,
			Settings: a.cfg.Settings,
		},
		connector.ProvisionTypeConfig,
	)
	if err != nil {
		return cerrors.Errorf("failed to create connector: %w", err)
	}

	// add processor IDs
	for _, proc := range a.cfg.Processors {
		_, err = a.connectorService.AddProcessor(ctx, a.cfg.ID, proc.ID)
		if err != nil {
			return cerrors.Errorf("failed to add processor %v: %w", proc.ID, err)
		}
	}

	return nil
}
func (a createConnectorAction) Rollback(ctx context.Context) error {
	err := a.connectorService.Delete(ctx, a.cfg.ID, a.pluginService)
	if !cerrors.Is(err, connector.ErrInstanceNotFound) {
		// ignore instance not found errors, this means the action failed to
		// create the connector in the first place
		return err
	}
	return nil
}
func (createConnectorAction) connectorType(t string) connector.Type {
	switch t {
	case config.TypeSource:
		return connector.TypeSource
	case config.TypeDestination:
		return connector.TypeDestination
	}
	return 0 // this is an invalid connector type, it will fail if we try to create it
}

type createProcessorAction struct {
	cfg    config.Processor
	parent processor.Parent

	processorService ProcessorService
}

func (a createProcessorAction) String() string {
	return fmt.Sprintf("create processor with ID %v", a.cfg.ID)
}
func (a createProcessorAction) Do(ctx context.Context) error {
	_, err := a.processorService.Create(
		ctx,
		a.cfg.ID,
		a.cfg.Type,
		a.parent,
		processor.Config{
			Settings: a.cfg.Settings,
			Workers:  a.cfg.Workers,
		},
		processor.ProvisionTypeConfig,
	)
	if err != nil {
		return cerrors.Errorf("failed to create processor: %w", err)
	}
	return nil
}
func (a createProcessorAction) Rollback(ctx context.Context) error {
	err := a.processorService.Delete(ctx, a.cfg.ID)
	if !cerrors.Is(err, processor.ErrInstanceNotFound) {
		// ignore instance not found errors, this means the action failed to
		// create the processor in the first place
		return err
	}
	return nil
}

// --------------------
// -- UPDATE ACTIONS --
// --------------------

type updatePipelineAction struct {
	oldConfig config.Pipeline
	newConfig config.Pipeline

	pipelineService PipelineService
}

func (a updatePipelineAction) String() string {
	return fmt.Sprintf("update pipeline with ID %v", a.oldConfig.ID)
}
func (a updatePipelineAction) Do(ctx context.Context) error {
	return a.update(ctx, a.newConfig)
}
func (a updatePipelineAction) Rollback(ctx context.Context) error {
	return a.update(ctx, a.oldConfig)
}
func (a updatePipelineAction) update(ctx context.Context, cfg config.Pipeline) error {
	p, err := a.pipelineService.Update(ctx, cfg.ID, pipeline.Config{
		Name:        cfg.Name,
		Description: cfg.Description,
	})
	if err != nil {
		return cerrors.Errorf("failed to update pipeline: %w", err)
	}
	_, err = a.pipelineService.UpdateDLQ(ctx, cfg.ID, pipeline.DLQ{
		Plugin:              cfg.DLQ.Plugin,               // TODO default
		Settings:            cfg.DLQ.Settings,             // TODO default
		WindowSize:          *cfg.DLQ.WindowSize,          // TODO nil pointer
		WindowNackThreshold: *cfg.DLQ.WindowNackThreshold, // TODO nil pointer
	})
	if err != nil {
		return cerrors.Errorf("failed to update pipeline DLQ: %w", err)
	}

	// update connector IDs
	if !a.isEqual(p.ConnectorIDs, cfg.Connectors) {
		// recreate all connector IDs
		for _, procID := range p.ConnectorIDs {
			_, err = a.pipelineService.RemoveConnector(ctx, p.ID, procID)
			if err != nil {
				return cerrors.Errorf("failed to remove connector %v: %w", procID, err)
			}
		}
		for _, proc := range cfg.Connectors {
			_, err = a.pipelineService.AddConnector(ctx, p.ID, proc.ID)
			if err != nil {
				return cerrors.Errorf("failed to add connector %v: %w", proc.ID, err)
			}
		}
	}

	return nil
}
func (updatePipelineAction) isEqual(ids []string, connectors []config.Connector) bool {
	if len(ids) != len(connectors) {
		return false
	}
	for i := range ids {
		if ids[i] != connectors[i].ID {
			return false
		}
	}
	return true
}

type updateConnectorAction struct {
	oldConfig config.Connector
	newConfig config.Connector

	connectorService ConnectorService
}

func (a updateConnectorAction) String() string {
	return fmt.Sprintf("update connector with ID %v", a.oldConfig.ID)
}
func (a updateConnectorAction) Do(ctx context.Context) error {
	return a.update(ctx, a.newConfig)
}
func (a updateConnectorAction) Rollback(ctx context.Context) error {
	return a.update(ctx, a.oldConfig)
}
func (a updateConnectorAction) update(ctx context.Context, cfg config.Connector) error {
	c, err := a.connectorService.Update(ctx, cfg.ID, connector.Config{
		Name:     cfg.Name,
		Settings: cfg.Settings,
	})
	if err != nil {
		return cerrors.Errorf("failed to update connector: %w", err)
	}

	// update processor IDs
	if !a.isEqual(c.ProcessorIDs, cfg.Processors) {
		// recreate all processor IDs
		for _, procID := range c.ProcessorIDs {
			_, err = a.connectorService.RemoveProcessor(ctx, c.ID, procID)
			if err != nil {
				return cerrors.Errorf("failed to remove processor %v: %w", procID, err)
			}
		}
		for _, proc := range cfg.Processors {
			_, err = a.connectorService.AddProcessor(ctx, c.ID, proc.ID)
			if err != nil {
				return cerrors.Errorf("failed to add processor %v: %w", proc.ID, err)
			}
		}
	}

	return nil
}
func (updateConnectorAction) isEqual(ids []string, processors []config.Processor) bool {
	if len(ids) != len(processors) {
		return false
	}
	for i := range ids {
		if ids[i] != processors[i].ID {
			return false
		}
	}
	return true
}

type updateProcessorAction struct {
	oldConfig config.Processor
	newConfig config.Processor

	processorService ProcessorService
}

func (a updateProcessorAction) String() string {
	return fmt.Sprintf("update processor with ID %v", a.oldConfig.ID)
}
func (a updateProcessorAction) Do(ctx context.Context) error {
	return a.update(ctx, a.newConfig)
}
func (a updateProcessorAction) Rollback(ctx context.Context) error {
	return a.update(ctx, a.oldConfig)
}
func (a updateProcessorAction) update(ctx context.Context, cfg config.Processor) error {
	_, err := a.processorService.Update(ctx, cfg.ID, processor.Config{
		Settings: cfg.Settings,
		Workers:  cfg.Workers,
	})
	if err != nil {
		return cerrors.Errorf("failed to update processor: %w", err)
	}
	return nil
}

// --------------------
// -- DELETE ACTIONS --
// --------------------

type deletePipelineAction createPipelineAction // piggyback on create action and reverse it

func (a deletePipelineAction) String() string {
	return fmt.Sprintf("delete pipeline with ID %v", a.cfg.ID)
}
func (a deletePipelineAction) Do(ctx context.Context) error {
	return createPipelineAction(a).Rollback(ctx)
}
func (a deletePipelineAction) Rollback(ctx context.Context) error {
	return createPipelineAction(a).Do(ctx)
}

type deleteConnectorAction createConnectorAction // piggyback on create action and reverse it

func (a deleteConnectorAction) String() string {
	return fmt.Sprintf("delete connector with ID %v", a.cfg.ID)
}
func (a deleteConnectorAction) Do(ctx context.Context) error {
	return createConnectorAction(a).Rollback(ctx)
}
func (a deleteConnectorAction) Rollback(ctx context.Context) error {
	return createConnectorAction(a).Do(ctx)
}

type deleteProcessorAction createProcessorAction // piggyback on create action and reverse it

func (a deleteProcessorAction) String() string {
	return fmt.Sprintf("delete processor with ID %v", a.cfg.ID)
}
func (a deleteProcessorAction) Do(ctx context.Context) error {
	return createProcessorAction(a).Rollback(ctx)
}
func (a deleteProcessorAction) Rollback(ctx context.Context) error {
	return createProcessorAction(a).Do(ctx)
}
