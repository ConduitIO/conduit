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

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/web/api/mock"
	apiv1 "github.com/conduitio/conduit/proto/gen/api/v1"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPipelineAPIv1_CreatePipeline(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := mock.NewPipelineOrchestrator(ctrl)
	api := NewPipelineAPIv1(psMock)

	config := pipeline.Config{
		Name:        "test-pipeline",
		Description: "description of my test pipeline",
	}
	pl := &pipeline.Instance{
		ID:     uuid.NewString(),
		Config: config,
		Status: pipeline.StatusSystemStopped,
	}

	psMock.EXPECT().Create(ctx, config).Return(pl, nil).Times(1)

	want := &apiv1.CreatePipelineResponse{
		Pipeline: &apiv1.Pipeline{
			Id: pl.ID,
			State: &apiv1.Pipeline_State{
				Status: apiv1.Pipeline_STATUS_STOPPED,
			},
			Config: &apiv1.Pipeline_Config{
				Name:        config.Name,
				Description: config.Description,
			},
			CreatedAt: timestamppb.New(pl.CreatedAt),
			UpdatedAt: timestamppb.New(pl.UpdatedAt),
		},
	}
	got, err := api.CreatePipeline(
		ctx,
		&apiv1.CreatePipelineRequest{
			Config: want.Pipeline.Config,
		},
	)

	assert.Ok(t, err)
	assert.Equal(t, want, got)
}

func TestPipelineAPIv1_ListPipelinesByName(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	psMock := mock.NewPipelineOrchestrator(ctrl)
	api := NewPipelineAPIv1(psMock)

	names := []string{"do-not-want-this-pipeline", "want-p1", "want-p2", "skip", "another-skipped"}
	pls := make([]*pipeline.Instance, 0)
	plsMap := make(map[string]*pipeline.Instance)

	for _, name := range names {
		p := &pipeline.Instance{
			ID:     uuid.NewString(),
			Config: pipeline.Config{Name: name},
		}
		pls = append(pls, p)
		plsMap[p.ID] = p
	}

	psMock.EXPECT().
		List(ctx).
		Return(plsMap).
		Times(1)

	want := &apiv1.ListPipelinesResponse{
		Pipelines: []*apiv1.Pipeline{{
			Id:    pls[1].ID,
			State: &apiv1.Pipeline_State{},
			Config: &apiv1.Pipeline_Config{
				Name: pls[1].Config.Name,
			},
			CreatedAt: timestamppb.New(pls[1].CreatedAt),
			UpdatedAt: timestamppb.New(pls[1].UpdatedAt),
		}, {
			Id:    pls[2].ID,
			State: &apiv1.Pipeline_State{},
			Config: &apiv1.Pipeline_Config{
				Name: pls[2].Config.Name,
			},
			CreatedAt: timestamppb.New(pls[2].CreatedAt),
			UpdatedAt: timestamppb.New(pls[2].UpdatedAt),
		}},
	}

	got, err := api.ListPipelines(
		ctx,
		&apiv1.ListPipelinesRequest{Name: "want-.*"},
	)
	assert.Ok(t, err)

	sortPipelines(want.Pipelines)
	sortPipelines(got.Pipelines)
	assert.Equal(t, want, got)
}

func sortPipelines(p []*apiv1.Pipeline) {
	sort.Slice(p, func(i, j int) bool {
		return p[i].Id < p[j].Id
	})
}
