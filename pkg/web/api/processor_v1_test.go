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

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/processor"
	procmock "github.com/conduitio/conduit/pkg/processor/mock"
	apimock "github.com/conduitio/conduit/pkg/web/api/mock"
	apiv1 "go.buf.build/conduitio/conduit/conduitio/conduit/api/v1"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProcessorAPIv1_ListProcessors(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(psMock)
	p := procmock.NewProcessor(ctrl)

	config := processor.Config{
		Settings: map[string]string{"titan": "armored"},
	}

	now := time.Now()
	prs := []*processor.Instance{
		{
			ID:   uuid.NewString(),
			Name: "Pants",
			Parent: processor.Parent{
				ID:   uuid.NewString(),
				Type: processor.ParentTypeConnector,
			},
			Config:    config,
			Processor: p,
			UpdatedAt: now,
			CreatedAt: now,
		},
		{
			ID:   uuid.NewString(),
			Name: "Pants Too",
			Parent: processor.Parent{
				ID:   uuid.NewString(),
				Type: processor.ParentTypeConnector,
			},
			Config:    config,
			Processor: p,
			UpdatedAt: now,
			CreatedAt: now,
		},
	}

	psMock.EXPECT().List(ctx).Return(map[string]*processor.Instance{
		prs[0].ID: prs[0],
		prs[1].ID: prs[1],
	}).Times(1)

	want := &apiv1.ListProcessorsResponse{Processors: []*apiv1.Processor{
		{
			Id:   prs[0].ID,
			Name: prs[0].Name,
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
			Id:   prs[1].ID,
			Name: prs[1].Name,
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

	assert.Ok(t, err)
	sortProcessors(want.Processors)
	sortProcessors(got.Processors)
	assert.Equal(t, want, got)
}

func TestProcessorAPIv1_ListProcessorsByParents(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(psMock)
	p := procmock.NewProcessor(ctrl)

	config := processor.Config{
		Settings: map[string]string{"titan": "armored"},
	}
	now := time.Now()

	sharedParent := uuid.NewString()
	prs := []*processor.Instance{
		{
			ID:   uuid.NewString(),
			Name: "Pants",
			Parent: processor.Parent{
				ID:   sharedParent,
				Type: processor.ParentTypeConnector,
			},
			Config:    config,
			Processor: p,
			UpdatedAt: now,
			CreatedAt: now,
		},
		{
			ID:   uuid.NewString(),
			Name: "Pants Too",
			Parent: processor.Parent{
				ID:   uuid.NewString(),
				Type: processor.ParentTypeConnector,
			},
			Config:    config,
			Processor: p,
			UpdatedAt: now,
			CreatedAt: now,
		},
		{
			ID:   uuid.NewString(),
			Name: "Pants Thrice",
			Parent: processor.Parent{
				ID:   uuid.NewString(),
				Type: processor.ParentTypePipeline,
			},
			Config:    processor.Config{},
			Processor: p,
			UpdatedAt: now,
			CreatedAt: now,
		},
		{
			ID:   uuid.NewString(),
			Name: "Shorts",
			Parent: processor.Parent{
				ID:   sharedParent,
				Type: processor.ParentTypePipeline,
			},
			Config:    processor.Config{},
			Processor: p,
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

	want := &apiv1.ListProcessorsResponse{Processors: []*apiv1.Processor{
		{
			Id:   prs[0].ID,
			Name: prs[0].Name,
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
			Id:   prs[2].ID,
			Name: prs[2].Name,
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
			Id:   prs[3].ID,
			Name: prs[3].Name,
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

	assert.Ok(t, err)
	sortProcessors(want.Processors)
	sortProcessors(got.Processors)
	assert.Equal(t, want, got)
}

func TestProcessorAPIv1_CreateProcessor(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(psMock)
	p := procmock.NewProcessor(ctrl)

	config := processor.Config{
		Settings: map[string]string{"titan": "armored"},
	}

	now := time.Now()
	pr := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "Pants",
		Parent: processor.Parent{
			ID:   uuid.NewString(),
			Type: processor.ParentTypeConnector,
		},
		Config:    config,
		Processor: p,
		UpdatedAt: now,
		CreatedAt: now,
	}
	psMock.EXPECT().Create(ctx, pr.Name, pr.Parent, config).Return(pr, nil).Times(1)

	want := &apiv1.CreateProcessorResponse{Processor: &apiv1.Processor{
		Id:   pr.ID,
		Name: pr.Name,
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

	got, err := api.CreateProcessor(
		ctx,
		&apiv1.CreateProcessorRequest{
			Name:   want.Processor.Name,
			Parent: want.Processor.Parent,
			Config: want.Processor.Config,
		},
	)

	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestProcessorAPIv1_GetProcessor(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(psMock)
	p := procmock.NewProcessor(ctrl)

	now := time.Now()
	pr := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "Pants",
		Parent: processor.Parent{
			ID:   uuid.NewString(),
			Type: processor.ParentTypeConnector,
		},
		Config: processor.Config{
			Settings: map[string]string{"titan": "armored"},
		},
		Processor: p,
		CreatedAt: now,
		UpdatedAt: now,
	}

	psMock.EXPECT().Get(ctx, pr.ID).Return(pr, nil).Times(1)

	want := &apiv1.GetProcessorResponse{Processor: &apiv1.Processor{
		Id:   pr.ID,
		Name: pr.Name,
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

	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestProcessorAPIv1_UpdateProcessor(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(psMock)
	p := procmock.NewProcessor(ctrl)

	config := processor.Config{
		Settings: map[string]string{"titan": "armored"},
	}

	now := time.Now()
	pr := &processor.Instance{
		ID:   uuid.NewString(),
		Name: "Pants",
		Parent: processor.Parent{
			ID:   uuid.NewString(),
			Type: processor.ParentTypeConnector,
		},
		Config:    config,
		Processor: p,
		UpdatedAt: now,
		CreatedAt: now,
	}
	psMock.EXPECT().Update(ctx, pr.ID, config).Return(pr, nil).Times(1)

	want := &apiv1.UpdateProcessorResponse{Processor: &apiv1.Processor{
		Id:   pr.ID,
		Name: pr.Name,
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

	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestProcessorAPIv1_DeleteProcessor(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := apimock.NewProcessorOrchestrator(ctrl)
	api := NewProcessorAPIv1(psMock)

	id := uuid.NewString()

	psMock.EXPECT().Delete(ctx, id).Return(nil).Times(1)

	want := &apiv1.DeleteProcessorResponse{}

	got, err := api.DeleteProcessor(
		ctx,
		&apiv1.DeleteProcessorRequest{
			Id: id,
		},
	)

	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func sortProcessors(c []*apiv1.Processor) {
	sort.Slice(c, func(i, j int) bool {
		return c[i].Id < c[j].Id
	})
}
