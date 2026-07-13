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
)

// action is a single action that can be executed or rolled back. Actions are
// used to import pipelines from configuration files and typically only affect
// a single entity (pipeline, connector or processor).
type action interface {
	String() string
	Do(context.Context) error
	Rollback(context.Context) error
	// Describe returns a stable, human/--json-renderable Change describing
	// what Do would execute, without executing anything. It reads only the
	// config the action already holds (see each implementation) — this is
	// what makes Plan (plan.go) a read-only preview of the exact same action
	// list ApplyPlan later runs, with no separate/divergent describe path.
	Describe() Change
}

// --------------------
// -- CREATE ACTIONS --
// --------------------

type createPipelineAction struct {
	cfg config.Pipeline
	// provisionedBy records how the pipeline was provisioned. Config-file
	// provisioning uses ProvisionTypeConfig (subject to reconciliation/deletion
	// on restart); programmatic callers of Import use ProvisionTypeAPI so their
	// pipelines survive restarts (see #1274).
	provisionedBy pipeline.ProvisionType

	pipelineService PipelineService
}

func (a createPipelineAction) String() string {
	return fmt.Sprintf("create pipeline with ID %v", a.cfg.ID)
}

// Describe returns EffectInPlace: a pipeline that does not exist yet has
// nothing running to disrupt. This also seeds Plan's brand-new-pipeline
// override (see plan.go), which forces every Change in an all-create Diff to
// EffectInPlace, including this pipeline's own nested connector/processor
// creates that would otherwise (in the general, existing-pipeline case)
// report EffectRestart.
func (a createPipelineAction) Describe() Change {
	return Change{
		Resource: ResourcePipeline,
		ID:       a.cfg.ID,
		Action:   ChangeActionCreate,
		Effect:   EffectInPlace,
		Code:     "provisioning.pipeline.create",
	}
}

func (a createPipelineAction) Do(ctx context.Context) error {
	_, err := a.pipelineService.Create(ctx, a.cfg.ID, pipeline.Config{
		Name:        a.cfg.Name,
		Description: a.cfg.Description,
	}, a.provisionedBy)
	if err != nil {
		return cerrors.Errorf("failed to create pipeline: %w", err)
	}
	_, err = a.pipelineService.UpdateDLQ(ctx, a.cfg.ID, pipeline.DLQ{
		Plugin:              a.cfg.DLQ.Plugin,
		Settings:            a.cfg.DLQ.Settings,
		WindowSize:          *a.cfg.DLQ.WindowSize,
		WindowNackThreshold: *a.cfg.DLQ.WindowNackThreshold,
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
	// ignore instance not found errors, this means the action failed to
	// create the pipeline in the first place
	if cerrors.Is(err, pipeline.ErrInstanceNotFound) {
		return nil
	}
	return err
}

type createConnectorAction struct {
	cfg        config.Connector
	pipelineID string

	connectorService       ConnectorService
	connectorPluginService ConnectorPluginService
}

func (a createConnectorAction) String() string {
	return fmt.Sprintf("create connector with ID %v", a.cfg.ID)
}

// Describe returns EffectRestart: adding a connector changes a pipeline's
// topology, which — for an existing pipeline — requires a restart to take
// effect. Plan overrides this to EffectInPlace when the whole pipeline is
// also being created for the first time (see plan.go's Plan doc).
func (a createConnectorAction) Describe() Change {
	return Change{
		Resource: ResourceConnector,
		ID:       a.cfg.ID,
		Action:   ChangeActionCreate,
		Effect:   EffectRestart,
		Code:     "provisioning.connector.create",
	}
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
	err := a.connectorService.Delete(ctx, a.cfg.ID, a.connectorPluginService)
	// ignore instance not found errors, this means the action failed to
	// create the connector in the first place
	if cerrors.Is(err, connector.ErrInstanceNotFound) {
		return nil
	}
	return err
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

// Describe returns EffectRestart: adding a processor changes a pipeline's
// (or connector's) processor chain, which requires a restart to take effect
// on an existing pipeline. Plan overrides this to EffectInPlace when the
// whole pipeline is also being created for the first time.
func (a createProcessorAction) Describe() Change {
	return Change{
		Resource: ResourceProcessor,
		ID:       a.cfg.ID,
		Action:   ChangeActionCreate,
		Effect:   EffectRestart,
		Code:     "provisioning.processor.create",
	}
}

func (a createProcessorAction) Do(ctx context.Context) error {
	_, err := a.processorService.Create(
		ctx,
		a.cfg.ID,
		a.cfg.Plugin,
		a.parent,
		processor.Config{
			Settings: a.cfg.Settings,
			Workers:  a.cfg.Workers,
		},
		processor.ProvisionTypeConfig,
		a.cfg.Condition,
	)
	if err != nil {
		return cerrors.Errorf("failed to create processor: %w", err)
	}
	return nil
}

func (a createProcessorAction) Rollback(ctx context.Context) error {
	err := a.processorService.Delete(ctx, a.cfg.ID)
	// ignore instance not found errors, this means the action failed to
	// create the processor in the first place
	if cerrors.Is(err, processor.ErrInstanceNotFound) {
		return nil
	}
	return err
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

// Describe reports which config.PipelineMutableFields changed (Status is
// excluded — config.PipelineIgnoredFields — matching what
// preparePipelineActions itself compares to decide an update is needed at
// all). Effect is EffectRestart when the pipeline's connector or processor
// membership changed (adding/removing a resource is a topology change,
// matching createConnectorAction/createProcessorAction's own classification
// and the design doc's UX example: "connectors changed" -> restart);
// otherwise EffectInPlace, since Name/Description/DLQ are metadata the
// running pipeline.Service.Update call can apply without stopping anything.
func (a updatePipelineAction) Describe() Change {
	effect := EffectInPlace
	if !equalConnectorIDs(a.oldConfig.Connectors, a.newConfig.Connectors) ||
		!equalProcessorIDs(a.oldConfig.Processors, a.newConfig.Processors) {
		effect = EffectRestart
	}
	return Change{
		Resource:    ResourcePipeline,
		ID:          a.oldConfig.ID,
		Action:      ChangeActionUpdate,
		Effect:      effect,
		ConfigPaths: diffPipelineFields(a.oldConfig, a.newConfig),
		Code:        "provisioning.pipeline.update",
	}
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
		Plugin:              cfg.DLQ.Plugin,
		Settings:            cfg.DLQ.Settings,
		WindowSize:          *cfg.DLQ.WindowSize,
		WindowNackThreshold: *cfg.DLQ.WindowNackThreshold,
	})
	if err != nil {
		return cerrors.Errorf("failed to update pipeline DLQ: %w", err)
	}

	// update connector IDs
	if !a.isEqualConnectors(p.ConnectorIDs, cfg.Connectors) {
		// Make a copy of the pipeline connectors, the instance value
		// will be modified during removal and can cause side effects.
		connectorIDs := make([]string, len(p.ConnectorIDs))
		_ = copy(connectorIDs, p.ConnectorIDs)

		// truncate pipeline connectors and add connectors from the pipeline config.
		for _, connID := range connectorIDs {
			_, err = a.pipelineService.RemoveConnector(ctx, cfg.ID, connID)
			if err != nil {
				return cerrors.Errorf("failed to remove connector %v: %w", connID, err)
			}
		}
		for _, conn := range cfg.Connectors {
			_, err = a.pipelineService.AddConnector(ctx, cfg.ID, conn.ID)
			if err != nil {
				return cerrors.Errorf("failed to add connector %v: %w", conn.ID, err)
			}
		}
	}

	// update processor IDs
	if !a.isEqualProcessors(p.ProcessorIDs, cfg.Processors) {
		// Make a copy of the pipeline processors, the instance value
		// will be modified during removal and can cause side effects.
		processorIDs := make([]string, len(p.ProcessorIDs))
		_ = copy(processorIDs, p.ProcessorIDs)

		// truncate pipeline processors and add processors from the pipeline config.
		for _, procID := range processorIDs {
			_, err = a.pipelineService.RemoveProcessor(ctx, cfg.ID, procID)
			if err != nil {
				return cerrors.Errorf("failed to remove processor %v: %w", procID, err)
			}
		}
		for _, proc := range cfg.Processors {
			_, err = a.pipelineService.AddProcessor(ctx, cfg.ID, proc.ID)
			if err != nil {
				return cerrors.Errorf("failed to add processor %v: %w", proc.ID, err)
			}
		}
	}

	return nil
}

func (updatePipelineAction) isEqualConnectors(ids []string, connectors []config.Connector) bool {
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

func (updatePipelineAction) isEqualProcessors(ids []string, processors []config.Processor) bool {
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

type updateConnectorAction struct {
	oldConfig config.Connector
	newConfig config.Connector

	connectorService ConnectorService
}

func (a updateConnectorAction) String() string {
	return fmt.Sprintf("update connector with ID %v", a.oldConfig.ID)
}

// Describe reports which config.ConnectorMutableFields changed. Effect is
// always EffectInPlace: prepareConnectorActions only ever builds an
// updateConnectorAction when config.ConnectorImmutableFields (Type) is
// unchanged — a Type change instead produces a delete+create pair (see
// deleteConnectorAction/createConnectorAction, both EffectRestart), so this
// action, by construction, never represents a topology-changing update.
func (a updateConnectorAction) Describe() Change {
	return Change{
		Resource:    ResourceConnector,
		ID:          a.oldConfig.ID,
		Action:      ChangeActionUpdate,
		Effect:      EffectInPlace,
		ConfigPaths: diffConnectorFields(a.oldConfig, a.newConfig),
		Code:        "provisioning.connector.update",
	}
}

func (a updateConnectorAction) Do(ctx context.Context) error {
	return a.update(ctx, a.newConfig)
}

func (a updateConnectorAction) Rollback(ctx context.Context) error {
	return a.update(ctx, a.oldConfig)
}

func (a updateConnectorAction) update(ctx context.Context, cfg config.Connector) error {
	c, err := a.connectorService.Update(ctx, cfg.ID, cfg.Plugin, connector.Config{
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
			_, err = a.connectorService.RemoveProcessor(ctx, cfg.ID, procID)
			if err != nil {
				return cerrors.Errorf("failed to remove processor %v: %w", procID, err)
			}
		}
		for _, proc := range cfg.Processors {
			_, err = a.connectorService.AddProcessor(ctx, cfg.ID, proc.ID)
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

// Describe reports which processor fields changed. Effect is EffectInPlace:
// processors have no immutable-field classification (see
// prepareProcessorActions — "all parts of a processor are updateable"), so
// every processor change, including Plugin, runs through
// processorService.Update rather than a delete+create pair.
func (a updateProcessorAction) Describe() Change {
	return Change{
		Resource:    ResourceProcessor,
		ID:          a.oldConfig.ID,
		Action:      ChangeActionUpdate,
		Effect:      EffectInPlace,
		ConfigPaths: diffProcessorFields(a.oldConfig, a.newConfig),
		Code:        "provisioning.processor.update",
	}
}

func (a updateProcessorAction) Do(ctx context.Context) error {
	return a.update(ctx, a.newConfig)
}

func (a updateProcessorAction) Rollback(ctx context.Context) error {
	return a.update(ctx, a.oldConfig)
}

func (a updateProcessorAction) update(ctx context.Context, cfg config.Processor) error {
	// UpdateWhileRunning, not Update: provisioning applies this action either on a
	// stopped pipeline (the restart path, where the processor isn't running so the
	// two are equivalent) or as part of a live in-place reconfigure, where the
	// processor IS running and applyInPlace immediately swaps the node to match
	// via lifecycle.ReconfigureProcessor. Update's running-instance guard exists
	// for direct API callers who do NOT swap the node; it must not block the
	// provisioning path, which always does. See processor.Service.UpdateWhileRunning.
	_, err := a.processorService.UpdateWhileRunning(
		ctx,
		cfg.ID,
		cfg.Plugin,
		processor.Config{
			Settings: cfg.Settings,
			Workers:  cfg.Workers,
		},
	)
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

// Describe returns EffectRestart: removing a pipeline entirely tears down
// any live work it was doing (Effect has no "stop"/"n/a" state to represent
// that more precisely — see the Effect doc), so it is treated as the more
// disruptive of the two buckets rather than silently as EffectInPlace.
func (a deletePipelineAction) Describe() Change {
	return Change{
		Resource: ResourcePipeline,
		ID:       a.cfg.ID,
		Action:   ChangeActionDelete,
		Effect:   EffectRestart,
		Code:     "provisioning.pipeline.delete",
	}
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

// Describe returns EffectRestart: removing a connector changes topology,
// same as createConnectorAction. This also covers the Type-change case
// (prepareConnectorActions pairs this with a createConnectorAction when
// config.ConnectorImmutableFields differ), which must always read as
// restart — never overridden to EffectInPlace outside Plan's brand-new
// -pipeline case.
func (a deleteConnectorAction) Describe() Change {
	return Change{
		Resource: ResourceConnector,
		ID:       a.cfg.ID,
		Action:   ChangeActionDelete,
		Effect:   EffectRestart,
		Code:     "provisioning.connector.delete",
	}
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

// Describe returns EffectRestart: removing a processor changes a pipeline's
// (or connector's) processor chain, same as createProcessorAction.
func (a deleteProcessorAction) Describe() Change {
	return Change{
		Resource: ResourceProcessor,
		ID:       a.cfg.ID,
		Action:   ChangeActionDelete,
		Effect:   EffectRestart,
		Code:     "provisioning.processor.delete",
	}
}

func (a deleteProcessorAction) Do(ctx context.Context) error {
	return createProcessorAction(a).Rollback(ctx)
}

func (a deleteProcessorAction) Rollback(ctx context.Context) error {
	return createProcessorAction(a).Do(ctx)
}
