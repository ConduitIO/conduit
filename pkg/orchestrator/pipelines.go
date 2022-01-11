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

package orchestrator

import (
	"context"

	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/google/uuid"
)

type PipelineOrchestrator base

func (s *PipelineOrchestrator) Start(ctx context.Context, id string) error {
	pl, err := s.pipelines.Get(ctx, id)
	if err != nil {
		return err
	}
	// TODO lock pipeline
	return s.pipelines.Start(ctx, s.connectors, s.processors, pl)
}

func (s *PipelineOrchestrator) Stop(ctx context.Context, id string) error {
	pl, err := s.pipelines.Get(ctx, id)
	if err != nil {
		return err
	}
	// TODO lock pipeline
	return s.pipelines.Stop(ctx, pl)
}

func (s *PipelineOrchestrator) List(ctx context.Context) map[string]*pipeline.Instance {
	return s.pipelines.List(ctx)
}

func (s *PipelineOrchestrator) Get(ctx context.Context, id string) (*pipeline.Instance, error) {
	return s.pipelines.Get(ctx, id)
}

func (s *PipelineOrchestrator) Create(ctx context.Context, cfg pipeline.Config) (*pipeline.Instance, error) {
	return s.pipelines.Create(ctx, uuid.NewString(), cfg)
}

func (s *PipelineOrchestrator) Update(ctx context.Context, id string, cfg pipeline.Config) (*pipeline.Instance, error) {
	pl, err := s.pipelines.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// TODO lock pipeline
	if pl.Status == pipeline.StatusRunning {
		return nil, pipeline.ErrPipelineRunning
	}
	return s.pipelines.Update(ctx, pl, cfg)
}

func (s *PipelineOrchestrator) Delete(ctx context.Context, id string) error {
	pl, err := s.pipelines.Get(ctx, id)
	if err != nil {
		return err
	}
	if pl.Status == pipeline.StatusRunning {
		return pipeline.ErrPipelineRunning
	}
	if len(pl.ConnectorIDs) != 0 {
		return ErrPipelineHasConnectorsAttached
	}
	if len(pl.ProcessorIDs) != 0 {
		return ErrPipelineHasProcessorsAttached
	}
	return s.pipelines.Delete(ctx, pl)
}
