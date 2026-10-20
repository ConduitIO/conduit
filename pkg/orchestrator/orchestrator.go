// Copyright © 2022 Meroxa, Inc.
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

//go:generate mockgen -typed -destination=mock/orchestrator.go -package=mock -mock_names=PipelineService=PipelineService,ConnectorService=ConnectorService,ProcessorService=ProcessorService,ConnectorPluginService=ConnectorPluginService,ProcessorPluginService=ProcessorPluginService,LifecycleService=LifecycleService . PipelineService,ConnectorService,ProcessorService,ConnectorPluginService,ProcessorPluginService,LifecycleService
package orchestrator

import (
	"context"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	processorSdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/processor"
)

type Orchestrator struct {
	Processors       *ProcessorOrchestrator
	Pipelines        *PipelineOrchestrator
	Connectors       *ConnectorOrchestrator
	ConnectorPlugins *ConnectorPluginOrchestrator
	ProcessorPlugins *ProcessorPluginOrchestrator
}

func NewOrchestrator(
	db database.DB,
	logger log.CtxLogger,
	pipelines PipelineService,
	connectors ConnectorService,
	processors ProcessorService,
	connectorPlugins ConnectorPluginService,
	processorPlugins ProcessorPluginService,
	lifecycle LifecycleService,
) *Orchestrator {
	b := base{
		db:               db,
		logger:           logger.WithComponent("orchestrator"),
		pipelines:        pipelines,
		connectors:       connectors,
		processors:       processors,
		connectorPlugins: connectorPlugins,
		processorPlugins: processorPlugins,
		lifecycle:        lifecycle,
	}

	return &Orchestrator{
		Processors:       (*ProcessorOrchestrator)(&b),
		Pipelines:        (*PipelineOrchestrator)(&b),
		Connectors:       (*ConnectorOrchestrator)(&b),
		ConnectorPlugins: (*ConnectorPluginOrchestrator)(&b),
		ProcessorPlugins: (*ProcessorPluginOrchestrator)(&b),
	}
}

type base struct {
	db     database.DB
	logger log.CtxLogger

	pipelines        PipelineService
	connectors       ConnectorService
	processors       ProcessorService
	connectorPlugins ConnectorPluginService
	processorPlugins ProcessorPluginService
	lifecycle        LifecycleService
}

type PipelineService interface {
	List(ctx context.Context) map[string]*pipeline.Instance
	Get(ctx context.Context, id string) (*pipeline.Instance, error)
	Create(ctx context.Context, id string, cfg pipeline.Config, p pipeline.ProvisionType) (*pipeline.Instance, error)
	Update(ctx context.Context, pipelineID string, cfg pipeline.Config) (*pipeline.Instance, error)
	Delete(ctx context.Context, pipelineID string) error
	UpdateDLQ(ctx context.Context, id string, dlq pipeline.DLQ) (*pipeline.Instance, error)

	AddConnector(ctx context.Context, pipelineID string, connectorID string) (*pipeline.Instance, error)
	RemoveConnector(ctx context.Context, pipelineID string, connectorID string) (*pipeline.Instance, error)
	AddProcessor(ctx context.Context, pipelineID string, processorID string) (*pipeline.Instance, error)
	RemoveProcessor(ctx context.Context, pipelineID string, processorID string) (*pipeline.Instance, error)
}

type ConnectorService interface {
	List(ctx context.Context) map[string]*connector.Instance
	Get(ctx context.Context, id string) (*connector.Instance, error)
	Create(ctx context.Context, id string, t connector.Type, plugin string, pipelineID string, c connector.Config, p connector.ProvisionType) (*connector.Instance, error)
	Delete(ctx context.Context, id string, dispenserFetcher connector.PluginDispenserFetcher) error
	// Restore puts back an instance Delete removed, keeping its state (#3137).
	Restore(ctx context.Context, instance *connector.Instance) error
	Update(ctx context.Context, id string, plugin string, c connector.Config) (*connector.Instance, error)

	AddProcessor(ctx context.Context, connectorID string, processorID string) (*connector.Instance, error)
	RemoveProcessor(ctx context.Context, connectorID string, processorID string) (*connector.Instance, error)
}

type ProcessorService interface {
	List(ctx context.Context) map[string]*processor.Instance
	Get(ctx context.Context, id string) (*processor.Instance, error)
	Create(ctx context.Context, id string, plugin string, parent processor.Parent, cfg processor.Config, p processor.ProvisionType, condition string) (*processor.Instance, error)
	MakeRunnableProcessor(ctx context.Context, i *processor.Instance) (*processor.RunnableProcessor, error)
	Update(ctx context.Context, id string, plugin string, cfg processor.Config) (*processor.Instance, error)
	Delete(ctx context.Context, id string) error
}

type ConnectorPluginService interface {
	List(ctx context.Context) (map[string]pconnector.Specification, error)
	NewDispenser(logger log.CtxLogger, name string, connectorID string) (connectorPlugin.Dispenser, error)
	ValidateSourceConfig(ctx context.Context, name string, settings map[string]string) error
	ValidateDestinationConfig(ctx context.Context, name string, settings map[string]string) error
}

type ProcessorPluginService interface {
	List(ctx context.Context) (map[string]processorSdk.Specification, error)
	RegisterStandalonePlugin(ctx context.Context, path string) (string, error)
}

type LifecycleService interface {
	// Start initiates a start of the given pipeline.
	Start(ctx context.Context, pipelineID string) error
	// Stop initiates a stop of the given pipeline. The method does not wait for
	// the pipeline (and its nodes) to actually stop.
	// When force is false the pipeline will try to stop gracefully and drain
	// any in-flight messages that have not yet reached the destination. When
	// force is true the pipeline will stop without draining in-flight messages.
	// It is allowed to execute a force stop even after a graceful stop was
	// requested.
	Stop(ctx context.Context, pipelineID string, force bool) error
	// IsActive reports whether the pipeline has a run that is starting,
	// live, or waiting out a recovery backoff, regardless of its status.
	IsActive(pipelineID string) bool
	// TryLockForMutation atomically checks that the pipeline has no run (the
	// IsActive test) and, if so, refuses every Start of it until unlock is
	// called, so a run cannot be admitted between the check and a config
	// write (#2961). It reports false, holding nothing, when the pipeline has
	// a run. unlock is idempotent. Hold it only across store writes.
	TryLockForMutation(pipelineID string) (unlock func(), ok bool)
}

// mutationMark holds the lifecycle's mutation mark for one orchestrator call
// (#2961). Declare it BEFORE the call's rollback.R and defer release before
// r.MustExecute, so deferred calls run rollback first and release the mark
// last: the compensating writes of a failed call are still config writes and
// must stay excluded from Start.
//
// Lock ordering: the mark is a counter under the lifecycle's publishMu, not a
// mutex the orchestrator holds. publishMu is taken only inside
// TryLockForMutation/unlock, as a leaf; the orchestrator takes no other lock
// of its own, and never calls the lifecycle (Start, Stop, StopAndWait) while
// marked. So there is no orchestrator-to-lifecycle lock cycle to form.
type mutationMark struct {
	unlock func()
}

// acquire marks pipelineID as being mutated, or returns the pipeline-running
// error msg describes when the pipeline has a run. A second acquire on the
// same mark is a programming error and keeps the first.
func (m *mutationMark) acquire(l LifecycleService, pipelineID, msg string) error {
	if m.unlock != nil {
		return nil
	}
	unlock, ok := l.TryLockForMutation(pipelineID)
	if !ok {
		// Invariant: errors.Is(err, ErrPipelineRunning) still holds — sentinel
		// wrapped, ConduitError adds the code.
		return pipelineRunningErr(msg)
	}
	m.unlock = unlock
	return nil
}

func (m *mutationMark) release() {
	if m.unlock != nil {
		m.unlock()
		m.unlock = nil
	}
}
