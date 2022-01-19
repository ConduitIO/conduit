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

//go:generate mockgen -destination=mock/processor.go -package=mock -mock_names=ProcessorOrchestrator=ProcessorOrchestrator . ProcessorOrchestrator

package api

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/web/api/fromproto"
	"github.com/conduitio/conduit/pkg/web/api/status"
	"github.com/conduitio/conduit/pkg/web/api/toproto"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/grpc"
)

// ProcessorOrchestrator defines a CRUD interface that manages the Processor resource.
type ProcessorOrchestrator interface {
	List(ctx context.Context) map[string]*processor.Instance
	// Get will return a single Processor or an error if it doesn't exist.
	Get(ctx context.Context, id string) (*processor.Instance, error)
	// Create will make a new Processor.
	Create(ctx context.Context, name string, t processor.Type, parent processor.Parent, cfg processor.Config) (*processor.Instance, error)
	// Update will update a Processor's config.
	Update(ctx context.Context, id string, cfg processor.Config) (*processor.Instance, error)
	// Delete removes a processor
	Delete(ctx context.Context, id string) error
}

type ProcessorAPIv1 struct {
	apiv1.UnimplementedProcessorServiceServer
	ps ProcessorOrchestrator
}

// NewProcessorAPIv1 returns a new processor API server.
func NewProcessorAPIv1(ps ProcessorOrchestrator) *ProcessorAPIv1 {
	return &ProcessorAPIv1{ps: ps}
}

// Register registers the service in the server.
func (p *ProcessorAPIv1) Register(srv *grpc.Server) {
	apiv1.RegisterProcessorServiceServer(srv, p)
}

func (p *ProcessorAPIv1) ListProcessors(
	ctx context.Context,
	req *apiv1.ListProcessorsRequest,
) (*apiv1.ListProcessorsResponse, error) {
	list := p.ps.List(ctx)
	var plist []*apiv1.Processor

	for _, v := range list {
		if len(req.ParentIds) == 0 || p.containsString(req.ParentIds, v.Parent.ID) {
			plist = append(plist, toproto.Processor(v))
		}
	}

	return &apiv1.ListProcessorsResponse{Processors: plist}, nil
}

// GetProcessor returns a single Processor proto response or an error.
func (p *ProcessorAPIv1) GetProcessor(
	ctx context.Context,
	req *apiv1.GetProcessorRequest,
) (*apiv1.GetProcessorResponse, error) {
	if req.Id == "" {
		return nil, cerrors.ErrEmptyID
	}

	// fetch the processor from the ProcessorOrchestrator
	pr, err := p.ps.Get(ctx, req.Id)
	if err != nil {
		return nil, status.ProcessorError(cerrors.Errorf("failed to get processor by ID: %w", err))
	}

	resp := toproto.Processor(pr)

	return &apiv1.GetProcessorResponse{Processor: resp}, nil
}

// CreateProcessor handles a CreateProcessorRequest, persists it to the Storage
// layer, and then returns the created processor with its assigned ID
func (p *ProcessorAPIv1) CreateProcessor(
	ctx context.Context,
	req *apiv1.CreateProcessorRequest,
) (*apiv1.CreateProcessorResponse, error) {
	created, err := p.ps.Create(
		ctx,
		req.Name,
		fromproto.ProcessorType(req.Type),
		fromproto.ProcessorParent(req.Parent),
		fromproto.ProcessorConfig(req.Config),
	)

	if err != nil {
		return nil, status.ProcessorError(cerrors.Errorf("failed to create processor: %w", err))
	}

	pr := toproto.Processor(created)

	return &apiv1.CreateProcessorResponse{Processor: pr}, nil
}

func (p *ProcessorAPIv1) UpdateProcessor(
	ctx context.Context,
	req *apiv1.UpdateProcessorRequest,
) (*apiv1.UpdateProcessorResponse, error) {
	if req.Id == "" {
		return nil, cerrors.ErrEmptyID
	}

	updated, err := p.ps.Update(ctx, req.Id, fromproto.ProcessorConfig(req.Config))

	if err != nil {
		return nil, status.ProcessorError(cerrors.Errorf("failed to update processor: %w", err))
	}

	pr := toproto.Processor(updated)

	return &apiv1.UpdateProcessorResponse{Processor: pr}, nil
}

func (p *ProcessorAPIv1) DeleteProcessor(ctx context.Context, req *apiv1.DeleteProcessorRequest) (*apiv1.DeleteProcessorResponse, error) {
	err := p.ps.Delete(ctx, req.Id)

	if err != nil {
		return nil, status.ProcessorError(cerrors.Errorf("failed to delete processor: %w", err))
	}

	return &apiv1.DeleteProcessorResponse{}, nil
}

func (p *ProcessorAPIv1) containsString(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
