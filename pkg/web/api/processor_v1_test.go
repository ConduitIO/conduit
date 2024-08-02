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

package api

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/cchan"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	opencdcv1 "github.com/conduitio/conduit-commons/proto/opencdc/v1"
	processorSdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/processor"
	apimock "github.com/conduitio/conduit/pkg/web/api/mock"
	"github.com/conduitio/conduit/pkg/web/api/toproto"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProcessorAPIv1_ListProcessors(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(psMock, nil)

	config := processor.Config{
		Settings: map[string]string{"titan": "armored"},
	}

	now := time.Now()
	prs := []*processor.Instance{
		{
			ID:     uuid.NewString(),
			Plugin: "Pants",
			Parent: processor.Parent{
				ID:   uuid.NewString(),
				Type: processor.ParentTypeConnector,
			},
			Config:    config,
			UpdatedAt: now,
			CreatedAt: now,
		},
		{
			ID:     uuid.NewString(),
			Plugin: "Pants Too",
			Parent: processor.Parent{
				ID:   uuid.NewString(),
				Type: processor.ParentTypeConnector,
			},
			Config:    config,
			UpdatedAt: now,
			CreatedAt: now,
		},
	}

	psMock.EXPECT().List(ctx).Return(map[string]*processor.Instance{
		prs[0].ID: prs[0],
		prs[1].ID: prs[1],
	}).Times(1)

	want := &apiv1.ListProcessorsResponse{
		Processors: []*apiv1.Processor{
			{
				Id:     prs[0].ID,
				Plugin: prs[0].Plugin,
				Config: &apiv1.Processor_Config{
					Settings: prs[0].Config.Settings,
				},
				Parent: &apiv1.Processor_Parent{
					Id:   prs[0].Parent.ID,
					Type: apiv1.Processor_Parent_Type(prs[0].Parent.Type),
				},
				CreatedAt: timestamppb.New(prs[0].CreatedAt),
				UpdatedAt: timestamppb.New(prs[0].UpdatedAt),
			},

			{
				Id:     prs[1].ID,
				Plugin: prs[1].Plugin,
				Config: &apiv1.Processor_Config{
					Settings: prs[1].Config.Settings,
				},
				Parent: &apiv1.Processor_Parent{
					Id:   prs[1].Parent.ID,
					Type: apiv1.Processor_Parent_Type(prs[1].Parent.Type),
				},
				CreatedAt: timestamppb.New(prs[1].CreatedAt),
				UpdatedAt: timestamppb.New(prs[1].UpdatedAt),
			},
		},
	}

	got, err := api.ListProcessors(ctx, &apiv1.ListProcessorsRequest{})

	is.NoErr(err)
	sortProcessors(want.Processors)
	sortProcessors(got.Processors)
	is.Equal(want, got)
}

func TestProcessorAPIv1_ListProcessorsByParents(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(psMock, nil)

	config := processor.Config{
		Settings: map[string]string{"titan": "armored"},
	}
	now := time.Now()

	sharedParent := uuid.NewString()
	prs := []*processor.Instance{
		{
			ID:     uuid.NewString(),
			Plugin: "Pants",
			Parent: processor.Parent{
				ID:   sharedParent,
				Type: processor.ParentTypeConnector,
			},
			Config:    config,
			UpdatedAt: now,
			CreatedAt: now,
		},
		{
			ID:     uuid.NewString(),
			Plugin: "Pants Too",
			Parent: processor.Parent{
				ID:   uuid.NewString(),
				Type: processor.ParentTypeConnector,
			},
			Config:    config,
			UpdatedAt: now,
			CreatedAt: now,
		},
		{
			ID:     uuid.NewString(),
			Plugin: "Pants Thrice",
			Parent: processor.Parent{
				ID:   uuid.NewString(),
				Type: processor.ParentTypePipeline,
			},
			Config:    processor.Config{},
			UpdatedAt: now,
			CreatedAt: now,
		},
		{
			ID:     uuid.NewString(),
			Plugin: "Shorts",
			Parent: processor.Parent{
				ID:   sharedParent,
				Type: processor.ParentTypePipeline,
			},
			Config:    processor.Config{},
			UpdatedAt: now,
			CreatedAt: now,
		},
	}

	psMock.EXPECT().List(ctx).Return(map[string]*processor.Instance{
		prs[0].ID: prs[0],
		prs[1].ID: prs[1],
		prs[2].ID: prs[2],
		prs[3].ID: prs[3],
	}).Times(1)

	want := &apiv1.ListProcessorsResponse{
		Processors: []*apiv1.Processor{
			{
				Id:     prs[0].ID,
				Plugin: prs[0].Plugin,
				Config: &apiv1.Processor_Config{
					Settings: prs[0].Config.Settings,
				},
				Parent: &apiv1.Processor_Parent{
					Id:   prs[0].Parent.ID,
					Type: apiv1.Processor_Parent_Type(prs[0].Parent.Type),
				},
				CreatedAt: timestamppb.New(prs[0].CreatedAt),
				UpdatedAt: timestamppb.New(prs[0].UpdatedAt),
			},

			{
				Id:     prs[2].ID,
				Plugin: prs[2].Plugin,
				Config: &apiv1.Processor_Config{
					Settings: prs[2].Config.Settings,
				},
				Parent: &apiv1.Processor_Parent{
					Id:   prs[2].Parent.ID,
					Type: apiv1.Processor_Parent_Type(prs[2].Parent.Type),
				},
				CreatedAt: timestamppb.New(prs[1].CreatedAt),
				UpdatedAt: timestamppb.New(prs[1].UpdatedAt),
			},
			{
				Id:     prs[3].ID,
				Plugin: prs[3].Plugin,
				Config: &apiv1.Processor_Config{
					Settings: prs[3].Config.Settings,
				},
				Parent: &apiv1.Processor_Parent{
					Id:   prs[3].Parent.ID,
					Type: apiv1.Processor_Parent_Type(prs[3].Parent.Type),
				},
				CreatedAt: timestamppb.New(prs[3].CreatedAt),
				UpdatedAt: timestamppb.New(prs[3].UpdatedAt),
			},
		},
	}

	got, err := api.ListProcessors(ctx, &apiv1.ListProcessorsRequest{ParentIds: []string{sharedParent, prs[2].Parent.ID}})

	is.NoErr(err)
	sortProcessors(want.Processors)
	sortProcessors(got.Processors)
	is.Equal(want, got)
}

func TestProcessorAPIv1_CreateProcessor(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(psMock, nil)

	config := processor.Config{
		Settings: map[string]string{"titan": "armored"},
	}

	now := time.Now()
	pr := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "Pants",
		Parent: processor.Parent{
			ID:   uuid.NewString(),
			Type: processor.ParentTypeConnector,
		},
		Config:    config,
		Condition: "{{ true }}",
		UpdatedAt: now,
		CreatedAt: now,
	}
	psMock.EXPECT().Create(ctx, pr.Plugin, pr.Parent, config, pr.Condition).Return(pr, nil).Times(1)

	want := &apiv1.CreateProcessorResponse{Processor: &apiv1.Processor{
		Id:     pr.ID,
		Plugin: pr.Plugin,
		Config: &apiv1.Processor_Config{
			Settings: pr.Config.Settings,
		},
		Parent: &apiv1.Processor_Parent{
			Id:   pr.Parent.ID,
			Type: apiv1.Processor_Parent_Type(pr.Parent.Type),
		},
		CreatedAt: timestamppb.New(pr.CreatedAt),
		UpdatedAt: timestamppb.New(pr.UpdatedAt),
		Condition: pr.Condition,
	}}

	got, err := api.CreateProcessor(
		ctx,
		&apiv1.CreateProcessorRequest{
			Plugin:    want.Processor.Plugin,
			Parent:    want.Processor.Parent,
			Config:    want.Processor.Config,
			Condition: want.Processor.Condition,
		},
	)

	is.NoErr(err)
	is.Equal(want, got)
}

func TestProcessorAPIv1_GetProcessor(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(psMock, nil)

	now := time.Now()
	pr := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "Pants",
		Parent: processor.Parent{
			ID:   uuid.NewString(),
			Type: processor.ParentTypeConnector,
		},
		Config: processor.Config{
			Settings: map[string]string{"titan": "armored"},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	psMock.EXPECT().Get(ctx, pr.ID).Return(pr, nil).Times(1)

	want := &apiv1.GetProcessorResponse{Processor: &apiv1.Processor{
		Id:     pr.ID,
		Plugin: pr.Plugin,
		Config: &apiv1.Processor_Config{
			Settings: pr.Config.Settings,
		},
		Parent: &apiv1.Processor_Parent{
			Id:   pr.Parent.ID,
			Type: apiv1.Processor_Parent_Type(pr.Parent.Type),
		},
		CreatedAt: timestamppb.New(pr.CreatedAt),
		UpdatedAt: timestamppb.New(pr.UpdatedAt),
	}}

	got, err := api.GetProcessor(
		ctx,
		&apiv1.GetProcessorRequest{
			Id: want.Processor.Id,
		},
	)

	is.NoErr(err)
	is.Equal(want, got)
}

func TestProcessorAPIv1_UpdateProcessor(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(psMock, nil)

	config := processor.Config{
		Settings: map[string]string{"titan": "armored"},
	}

	now := time.Now()
	pr := &processor.Instance{
		ID:     uuid.NewString(),
		Plugin: "Pants",
		Parent: processor.Parent{
			ID:   uuid.NewString(),
			Type: processor.ParentTypeConnector,
		},
		Config:    config,
		UpdatedAt: now,
		CreatedAt: now,
	}
	psMock.EXPECT().Update(ctx, pr.ID, config).Return(pr, nil).Times(1)

	want := &apiv1.UpdateProcessorResponse{Processor: &apiv1.Processor{
		Id:     pr.ID,
		Plugin: pr.Plugin,
		Config: &apiv1.Processor_Config{
			Settings: pr.Config.Settings,
		},
		Parent: &apiv1.Processor_Parent{
			Id:   pr.Parent.ID,
			Type: apiv1.Processor_Parent_Type(pr.Parent.Type),
		},
		CreatedAt: timestamppb.New(pr.CreatedAt),
		UpdatedAt: timestamppb.New(pr.UpdatedAt),
	}}

	got, err := api.UpdateProcessor(
		ctx,
		&apiv1.UpdateProcessorRequest{
			Id:     want.Processor.Id,
			Config: want.Processor.Config,
		},
	)

	is.NoErr(err)
	is.Equal(want, got)
}

func TestProcessorAPIv1_DeleteProcessor(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(psMock, nil)

	id := uuid.NewString()

	psMock.EXPECT().Delete(ctx, id).Return(nil).Times(1)

	want := &apiv1.DeleteProcessorResponse{}

	got, err := api.DeleteProcessor(
		ctx,
		&apiv1.DeleteProcessorRequest{
			Id: id,
		},
	)

	is.NoErr(err)
	is.Equal(want, got)
}

func TestProcessorAPIv1_InspectIn_SendRecord(t *testing.T) {
	is := is.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctrl := gomock.NewController(t)
	orchestrator := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(orchestrator, nil)

	id := uuid.NewString()
	rec := generateTestRecord()
	recProto := &opencdcv1.Record{}
	err := rec.ToProto(recProto)
	is.NoErr(err)

	ins := inspector.New(log.Nop(), 10)
	session := ins.NewSession(ctx, "test-id")

	orchestrator.EXPECT().
		InspectIn(ctx, id).
		Return(session, nil).
		Times(1)

	inspectChan := make(chan struct{})
	inspectServer := apimock.NewProcessorService_InspectProcessorInServer(ctrl)
	inspectServer.EXPECT().
		Send(&apiv1.InspectProcessorInResponse{Record: recProto}).
		DoAndReturn(func(_ *apiv1.InspectProcessorInResponse) error {
			close(inspectChan)
			return nil
		})
	inspectServer.EXPECT().Context().Return(ctx).AnyTimes()

	go func() {
		_ = api.InspectProcessorIn(
			&apiv1.InspectProcessorInRequest{Id: id},
			inspectServer,
		)
	}()
	ins.Send(ctx, []opencdc.Record{rec})

	_, ok, err := cchan.ChanOut[struct{}](inspectChan).RecvTimeout(ctx, 100*time.Millisecond)
	is.NoErr(err)
	is.True(!ok)
}

func TestProcessorAPIv1_InspectIn_SendErr(t *testing.T) {
	is := is.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctrl := gomock.NewController(t)
	orchestrator := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(orchestrator, nil)
	id := uuid.NewString()

	ins := inspector.New(log.Nop(), 10)
	session := ins.NewSession(ctx, "test-id")

	orchestrator.EXPECT().
		InspectIn(ctx, id).
		Return(session, nil).
		Times(1)

	inspectServer := apimock.NewProcessorService_InspectProcessorInServer(ctrl)
	inspectServer.EXPECT().Context().Return(ctx).AnyTimes()
	errSend := cerrors.New("I'm sorry, but no.")
	inspectServer.EXPECT().Send(gomock.Any()).Return(errSend)

	errC := make(chan error)
	go func() {
		err := api.InspectProcessorIn(
			&apiv1.InspectProcessorInRequest{Id: id},
			inspectServer,
		)
		errC <- err
	}()
	ins.Send(ctx, []opencdc.Record{generateTestRecord()})

	err, b, err2 := cchan.ChanOut[error](errC).RecvTimeout(context.Background(), 100*time.Millisecond)
	is.NoErr(err2)
	is.True(b)                        // expected to receive an error
	is.True(cerrors.Is(err, errSend)) // expected 'I'm sorry, but no.' error"
}

func TestProcessorAPIv1_InspectIn_Err(t *testing.T) {
	is := is.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctrl := gomock.NewController(t)
	orchestrator := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(orchestrator, nil)
	id := uuid.NewString()
	err := cerrors.New("not found, sorry")

	orchestrator.EXPECT().
		InspectIn(ctx, gomock.Any()).
		Return(nil, err).
		Times(1)

	inspectServer := apimock.NewProcessorService_InspectProcessorInServer(ctrl)
	inspectServer.EXPECT().Context().Return(ctx).AnyTimes()

	errAPI := api.InspectProcessorIn(
		&apiv1.InspectProcessorInRequest{Id: id},
		inspectServer,
	)
	is.True(errAPI != nil)
	is.Equal(
		"rpc error: code = Internal desc = failed to inspect processor: not found, sorry",
		errAPI.Error(),
	)
}

func TestProcessorAPIv1_ListProcessorPluginsByName(t *testing.T) {
	is := is.New(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	ppoMock := apimock.NewProcessorPluginOrchestrator(ctrl)
	api := NewProcessorAPIv1(nil, ppoMock)

	names := []string{"do-not-want-this-plugin", "want-p1", "want-p2", "skip", "another-skipped"}

	plsMap := make(map[string]processorSdk.Specification)
	pls := make([]processorSdk.Specification, 0)

	for _, name := range names {
		ps := processorSdk.Specification{
			Name:        name,
			Description: "desc",
			Version:     "v1.0",
			Author:      "Aaron",
			Parameters: map[string]config.Parameter{
				"param": {
					Type: config.ParameterTypeString,
					Validations: []config.Validation{
						config.ValidationRequired{},
					},
				},
			},
		}
		pls = append(pls, ps)
		plsMap[name] = ps
	}

	ppoMock.EXPECT().
		List(ctx).
		Return(plsMap, nil).
		Times(1)

	want := &apiv1.ListProcessorPluginsResponse{
		Plugins: []*apiv1.ProcessorPluginSpecifications{
			toproto.ProcessorPluginSpecifications(pls[1].Name, pls[1]),
			toproto.ProcessorPluginSpecifications(pls[2].Name, pls[2]),
		},
	}

	got, err := api.ListProcessorPlugins(
		ctx,
		&apiv1.ListProcessorPluginsRequest{Name: "want-.*"},
	)

	is.NoErr(err)

	sortPlugins := func(p []*apiv1.ProcessorPluginSpecifications) {
		sort.Slice(p, func(i, j int) bool {
			return p[i].Name < p[j].Name
		})
	}

	sortPlugins(want.Plugins)
	sortPlugins(got.Plugins)
	is.Equal(want, got)
}

func sortProcessors(c []*apiv1.Processor) {
	sort.Slice(c, func(i, j int) bool {
		return c[i].Id < c[j].Id
	})
}
