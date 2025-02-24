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

//go:generate mockgen -typed -destination=mock/processor.go -package=mock -mock_names=ProcessorOrchestrator=ProcessorOrchestrator . ProcessorOrchestrator
//go:generate mockgen -typed -destination=mock/processor_service_in.go -package=mock -mock_names=ProcessorService_InspectProcessorInServer=ProcessorService_InspectProcessorInServer github.com/conduitio/conduit/proto/api/v1 ProcessorService_InspectProcessorInServer
//go:generate mockgen -typed -destination=mock/processor_service_out.go -package=mock -mock_names=ProcessorService_InspectProcessorOutServer=ProcessorService_InspectProcessorOutServer github.com/conduitio/conduit/proto/api/v1 ProcessorService_InspectProcessorOutServer
//go:generate mockgen -typed -destination=mock/processor_plugin.go -package=mock -mock_names=ProcessorPluginOrchestrator=ProcessorPluginOrchestrator . ProcessorPluginOrchestrator

package api

import (
	"context"
	"regexp"
	"slices"

	opencdcv1 "github.com/conduitio/conduit-commons/proto/opencdc/v1"
	processorSdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/http/api/fromproto"
	"github.com/conduitio/conduit/pkg/http/api/status"
	"github.com/conduitio/conduit/pkg/http/api/toproto"
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/processor"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/grpc"
)

// ProcessorOrchestrator defines a CRUD interface that manages the processor resource.
type ProcessorOrchestrator interface {
	List(ctx context.Context) map[string]*processor.Instance
	// Get will return a single processor or an error if it doesn't exist.
	Get(ctx context.Context, id string) (*processor.Instance, error)
	// Create will make a new processor.
	Create(ctx context.Context, procType string, parent processor.Parent, cfg processor.Config, condition string) (*processor.Instance, error)
	// Update will update a processor's config.
	Update(ctx context.Context, id string, plugin string, cfg processor.Config) (*processor.Instance, error)
	// Delete removes a processor
	Delete(ctx context.Context, id string) error
	// InspectIn starts an inspector session for the records coming into the processor with given ID.
	InspectIn(ctx context.Context, id string) (*inspector.Session, error)
	// InspectOut starts an inspector session for the records going out of the processor with given ID.
	InspectOut(ctx context.Context, id string) (*inspector.Session, error)
}

type ProcessorPluginOrchestrator interface {
	// List will return all processor plugins' specs.
	List(ctx context.Context) (map[string]processorSdk.Specification, error)
}

type ProcessorAPIv1 struct {
	apiv1.UnimplementedProcessorServiceServer
	processorOrchestrator       ProcessorOrchestrator
	processorPluginOrchestrator ProcessorPluginOrchestrator
}

// NewProcessorAPIv1 returns a new processor API server.
func NewProcessorAPIv1(
	po ProcessorOrchestrator,
	ppo ProcessorPluginOrchestrator,
) *ProcessorAPIv1 {
	return &ProcessorAPIv1{
		processorOrchestrator:       po,
		processorPluginOrchestrator: ppo,
	}
}

// Register registers the service in the server.
func (p *ProcessorAPIv1) Register(srv *grpc.Server) {
	apiv1.RegisterProcessorServiceServer(srv, p)
}

func (p *ProcessorAPIv1) ListProcessors(
	ctx context.Context,
	req *apiv1.ListProcessorsRequest,
) (*apiv1.ListProcessorsResponse, error) {
	list := p.processorOrchestrator.List(ctx)
	var plist []*apiv1.Processor

	for _, v := range list {
		if len(req.ParentIds) == 0 || slices.Contains(req.ParentIds, v.Parent.ID) {
			plist = append(plist, toproto.Processor(v))
		}
	}

	return &apiv1.ListProcessorsResponse{Processors: plist}, nil
}

func (p *ProcessorAPIv1) InspectProcessorIn(
	req *apiv1.InspectProcessorInRequest,
	server apiv1.ProcessorService_InspectProcessorInServer,
) error {
	if req.Id == "" {
		return status.ProcessorError(cerrors.ErrEmptyID)
	}

	session, err := p.processorOrchestrator.InspectIn(server.Context(), req.Id)
	if err != nil {
		return status.ProcessorError(cerrors.Errorf("failed to inspect processor: %w", err))
	}

	for rec := range session.C {
		recProto := &opencdcv1.Record{}
		err := rec.ToProto(recProto)
		if err != nil {
			return cerrors.Errorf("failed converting record: %w", err)
		}

		err = server.Send(&apiv1.InspectProcessorInResponse{
			Record: recProto,
		})
		if err != nil {
			return cerrors.Errorf("failed sending record: %w", err)
		}
	}

	return cerrors.New("inspector session closed")
}

func (p *ProcessorAPIv1) InspectProcessorOut(
	req *apiv1.InspectProcessorOutRequest,
	server apiv1.ProcessorService_InspectProcessorOutServer,
) error {
	if req.Id == "" {
		return status.ProcessorError(cerrors.ErrEmptyID)
	}

	session, err := p.processorOrchestrator.InspectOut(server.Context(), req.Id)
	if err != nil {
		return status.ProcessorError(cerrors.Errorf("failed to inspect processor: %w", err))
	}

	for rec := range session.C {
		recProto := &opencdcv1.Record{}
		err := rec.ToProto(recProto)
		if err != nil {
			return cerrors.Errorf("failed converting record: %w", err)
		}

		err = server.Send(&apiv1.InspectProcessorOutResponse{
			Record: recProto,
		})
		if err != nil {
			return cerrors.Errorf("failed sending record: %w", err)
		}
	}

	return cerrors.New("inspector session closed")
}

// GetProcessor returns a single Interface proto response or an error.
func (p *ProcessorAPIv1) GetProcessor(
	ctx context.Context,
	req *apiv1.GetProcessorRequest,
) (*apiv1.GetProcessorResponse, error) {
	if req.Id == "" {
		return nil, cerrors.ErrEmptyID
	}

	// fetch the processor from the ProcessorOrchestrator
	pr, err := p.processorOrchestrator.Get(ctx, req.Id)
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
	//nolint:staticcheck // we're fine with allowing Type for some time more
	if req.Type != "" && req.Plugin != "" {
		return nil, status.ProcessorError(cerrors.New("only one of [type, plugin] can be specified"))
	}
	plugin := req.Plugin
	if plugin == "" {
		//nolint:staticcheck // we're fine with allowing Type for some time more
		plugin = req.Type
	}

	created, err := p.processorOrchestrator.Create(
		ctx,
		plugin,
		fromproto.ProcessorParent(req.Parent),
		fromproto.ProcessorConfig(req.Config),
		req.Condition,
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

	updated, err := p.processorOrchestrator.Update(ctx, req.Id, req.Plugin, fromproto.ProcessorConfig(req.Config))
	if err != nil {
		return nil, status.ProcessorError(cerrors.Errorf("failed to update processor: %w", err))
	}

	pr := toproto.Processor(updated)

	return &apiv1.UpdateProcessorResponse{Processor: pr}, nil
}

func (p *ProcessorAPIv1) DeleteProcessor(ctx context.Context, req *apiv1.DeleteProcessorRequest) (*apiv1.DeleteProcessorResponse, error) {
	err := p.processorOrchestrator.Delete(ctx, req.Id)
	if err != nil {
		return nil, status.ProcessorError(cerrors.Errorf("failed to delete processor: %w", err))
	}

	return &apiv1.DeleteProcessorResponse{}, nil
}

func (p *ProcessorAPIv1) ListProcessorPlugins(
	ctx context.Context,
	req *apiv1.ListProcessorPluginsRequest,
) (*apiv1.ListProcessorPluginsResponse, error) {
	var nameFilter *regexp.Regexp
	if req.GetName() != "" {
		var err error
		nameFilter, err = regexp.Compile("^" + req.GetName() + "$")
		if err != nil {
			return nil, status.PluginError(cerrors.New("invalid name regex"))
		}
	}

	mp, err := p.processorPluginOrchestrator.List(ctx)
	if err != nil {
		return nil, status.PluginError(err)
	}
	var plist []*apiv1.ProcessorPluginSpecifications

	for name, v := range mp {
		if nameFilter != nil && !nameFilter.MatchString(name) {
			continue // don't add to result list, filter didn't match
		}
		plist = append(plist, toproto.ProcessorPluginSpecifications(name, v))
	}

	return &apiv1.ListProcessorPluginsResponse{Plugins: plist}, nil
}
