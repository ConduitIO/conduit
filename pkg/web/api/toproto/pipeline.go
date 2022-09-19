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

package toproto

import (
	"github.com/conduitio/conduit/pkg/pipeline"
	apiv1 "go.buf.build/conduitio/conduit/conduitio/conduit/api/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func Pipeline(in *pipeline.Instance) *apiv1.Pipeline {
	return &apiv1.Pipeline{
		Id: in.ID,
		State: &apiv1.Pipeline_State{
			Status: PipelineStatus(in.Status),
			Error:  in.Error,
		},
		Config:       PipelineConfig(in.Config),
		CreatedAt:    timestamppb.New(in.CreatedAt),
		UpdatedAt:    timestamppb.New(in.UpdatedAt),
		ConnectorIds: in.ConnectorIDs,
		ProcessorIds: in.ProcessorIDs,
	}
}

func PipelineConfig(in pipeline.Config) *apiv1.Pipeline_Config {
	return &apiv1.Pipeline_Config{
		Name:        in.Name,
		Description: in.Description,
	}
}

func PipelineStatus(in pipeline.Status) apiv1.Pipeline_Status {
	switch in {
	case pipeline.StatusRunning:
		return apiv1.Pipeline_STATUS_RUNNING
	case pipeline.StatusUserStopped:
		return apiv1.Pipeline_STATUS_STOPPED
	case pipeline.StatusSystemStopped:
		return apiv1.Pipeline_STATUS_STOPPED
	case pipeline.StatusDegraded:
		return apiv1.Pipeline_STATUS_DEGRADED
	}
	return apiv1.Pipeline_STATUS_UNSPECIFIED
}
