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

//go:generate mockgen -typed -destination=mock/pipeline.go -package=mock -mock_names=PipelineOrchestrator=PipelineOrchestrator . PipelineOrchestrator

package api

import (
	"context"
	"regexp"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/http/api/fromproto"
	"github.com/conduitio/conduit/pkg/http/api/status"
	"github.com/conduitio/conduit/pkg/http/api/toproto"
	"github.com/conduitio/conduit/pkg/pipeline"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/grpc"
)

// PipelineOrchestrator defines a CRUD interface that manages the Pipeline resource.
type PipelineOrchestrator interface {
	// Start runs a pipeline.
	Start(ctx context.Context, id string) error
	// Stop stops a pipeline.
	Stop(ctx context.Context, id string, force bool) error
	// List will return all pipelines stored in it.
	List(ctx context.Context) map[string]*pipeline.Instance
	// Get will return a single Pipeline or an error if it doesn't exist.
	Get(ctx context.Context, id string) (*pipeline.Instance, error)
	// Create will make a new Pipeline.
	Create(ctx context.Context, cfg pipeline.Config) (*pipeline.Instance, error)
	// Update will update a Pipeline's config.
	Update(ctx context.Context, id string, cfg pipeline.Config) (*pipeline.Instance, error)
	// UpdateDLQ will update a Pipeline's dead-letter-queue.
	UpdateDLQ(ctx context.Context, id string, dlq pipeline.DLQ) (*pipeline.Instance, error)
	// Delete removes a pipeline and all associated connectors and plugins.
	Delete(ctx context.Context, id string) error
}

type PipelineAPIv1 struct {
	apiv1.UnimplementedPipelineServiceServer
	ps PipelineOrchestrator
}

// NewPipelineAPIv1 returns a new pipeline API server.
func NewPipelineAPIv1(ps PipelineOrchestrator) *PipelineAPIv1 {
	return &PipelineAPIv1{ps: ps}
}

// Register registers the service in the server.
func (p *PipelineAPIv1) Register(srv *grpc.Server) {
	apiv1.RegisterPipelineServiceServer(srv, p)
}

// GetPipeline returns a single Pipeline proto response or an error.
func (p *PipelineAPIv1) GetPipeline(
	ctx context.Context,
	req *apiv1.GetPipelineRequest,
) (*apiv1.GetPipelineResponse, error) {
	if req.Id == "" {
		return nil, status.PipelineError(cerrors.ErrEmptyID)
	}

	pl, err := p.ps.Get(ctx, req.Id)
	if err != nil {
		return nil, status.PipelineError(cerrors.Errorf("failed to get pipeline by ID: %w", err))
	}

	resp := toproto.Pipeline(pl)

	return &apiv1.GetPipelineResponse{Pipeline: resp}, nil
}

// ListPipelines returns a list of all pipelines.
func (p *PipelineAPIv1) ListPipelines(
	ctx context.Context,
	req *apiv1.ListPipelinesRequest,
) (*apiv1.ListPipelinesResponse, error) {
	var nameFilter *regexp.Regexp
	if req.GetName() != "" {
		var err error
		nameFilter, err = regexp.Compile("^" + req.GetName() + "$")
		if err != nil {
			return nil, status.PipelineError(cerrors.New("invalid name regex"))
		}
	}

	list := p.ps.List(ctx)
	var plist []*apiv1.Pipeline

	for _, v := range list {
		if nameFilter != nil && !nameFilter.MatchString(v.Config.Name) {
			continue // don't add to result list, filter didn't match
		}
		plist = append(plist, toproto.Pipeline(v))
	}

	return &apiv1.ListPipelinesResponse{Pipelines: plist}, nil
}

// CreatePipeline handles a CreatePipelineRequest, persists it to the Storage
// layer, and then returns the created pipeline with its assigned ID
func (p *PipelineAPIv1) CreatePipeline(
	ctx context.Context,
	req *apiv1.CreatePipelineRequest,
) (*apiv1.CreatePipelineResponse, error) {
	// translate proto request to persistent config
	cfg := fromproto.PipelineConfig(req.Config)

	// create the pipeline
	created, err := p.ps.Create(ctx, cfg)
	if err != nil {
		return nil, status.PipelineError(cerrors.Errorf("failed to create pipeline: %w", err))
	}

	// translate persisted config to proto response
	pl := toproto.Pipeline(created)

	return &apiv1.CreatePipelineResponse{Pipeline: pl}, nil
}

func (p *PipelineAPIv1) UpdatePipeline(
	ctx context.Context,
	req *apiv1.UpdatePipelineRequest,
) (*apiv1.UpdatePipelineResponse, error) {
	if req.Id == "" {
		return nil, status.PipelineError(cerrors.ErrEmptyID)
	}

	cfg := fromproto.PipelineConfig(req.Config)
	updated, err := p.ps.Update(ctx, req.Id, cfg)
	if err != nil {
		return nil, status.PipelineError(cerrors.Errorf("failed to update pipeline: %w", err))
	}

	pl := toproto.Pipeline(updated)

	return &apiv1.UpdatePipelineResponse{Pipeline: pl}, nil
}

func (p *PipelineAPIv1) DeletePipeline(
	ctx context.Context,
	req *apiv1.DeletePipelineRequest,
) (*apiv1.DeletePipelineResponse, error) {
	err := p.ps.Delete(ctx, req.Id)
	if err != nil {
		return nil, status.PipelineError(cerrors.Errorf("failed to delete pipeline: %w", err))
	}

	return &apiv1.DeletePipelineResponse{}, nil
}

func (p *PipelineAPIv1) StartPipeline(
	ctx context.Context,
	req *apiv1.StartPipelineRequest,
) (*apiv1.StartPipelineResponse, error) {
	err := p.ps.Start(ctx, req.Id)
	if err != nil {
		return nil, status.PipelineError(cerrors.Errorf("failed to start pipeline: %w", err))
	}

	return &apiv1.StartPipelineResponse{}, nil
}

func (p *PipelineAPIv1) StopPipeline(
	ctx context.Context,
	req *apiv1.StopPipelineRequest,
) (*apiv1.StopPipelineResponse, error) {
	err := p.ps.Stop(ctx, req.Id, req.Force)
	if err != nil {
		return nil, status.PipelineError(cerrors.Errorf("failed to stop pipeline: %w", err))
	}

	return &apiv1.StopPipelineResponse{}, nil
}

func (p *PipelineAPIv1) GetDLQ(
	ctx context.Context,
	req *apiv1.GetDLQRequest,
) (*apiv1.GetDLQResponse, error) {
	if req.Id == "" {
		return nil, status.PipelineError(cerrors.ErrEmptyID)
	}

	pl, err := p.ps.Get(ctx, req.Id)
	if err != nil {
		return nil, status.PipelineError(cerrors.Errorf("failed to get pipeline by ID: %w", err))
	}

	resp := toproto.PipelineDLQ(pl.DLQ)

	return &apiv1.GetDLQResponse{Dlq: resp}, nil
}

func (p *PipelineAPIv1) UpdateDLQ(
	ctx context.Context,
	req *apiv1.UpdateDLQRequest,
) (*apiv1.UpdateDLQResponse, error) {
	if req.Id == "" {
		return nil, status.PipelineError(cerrors.ErrEmptyID)
	}

	cfg := fromproto.PipelineDLQ(req.Dlq)
	updated, err := p.ps.UpdateDLQ(ctx, req.Id, cfg)
	if err != nil {
		return nil, status.PipelineError(cerrors.Errorf("failed to update pipeline dead-letter-queue: %w", err))
	}

	dlq := toproto.PipelineDLQ(updated.DLQ)

	return &apiv1.UpdateDLQResponse{Dlq: dlq}, nil
}

func (p *PipelineAPIv1) ImportPipeline(context.Context, *apiv1.ImportPipelineRequest) (*apiv1.ImportPipelineResponse, error) {
	return &apiv1.ImportPipelineResponse{}, cerrors.ErrNotImpl
}

func (p *PipelineAPIv1) ExportPipeline(context.Context, *apiv1.ExportPipelineRequest) (*apiv1.ExportPipelineResponse, error) {
	return &apiv1.ExportPipelineResponse{}, cerrors.ErrNotImpl
}
