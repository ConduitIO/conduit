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
	"github.com/conduitio/conduit/pkg/connector"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var cTypes [1]struct{}
	_ = cTypes[int(connector.TypeSource)-int(apiv1.Connector_TYPE_SOURCE)]
	_ = cTypes[int(connector.TypeDestination)-int(apiv1.Connector_TYPE_DESTINATION)]
}

func Connector(in *connector.Instance) *apiv1.Connector {
	apiConnector := &apiv1.Connector{
		Id:           in.ID,
		CreatedAt:    timestamppb.New(in.CreatedAt),
		UpdatedAt:    timestamppb.New(in.UpdatedAt),
		Config:       ConnectorConfig(in.Config),
		Plugin:       in.Plugin,
		PipelineId:   in.PipelineID,
		ProcessorIds: in.ProcessorIDs,
		Type:         ConnectorType(in.Type),
	}
	if in.State != nil {
		switch in.Type {
		case connector.TypeSource:
			apiConnector.State = ConnectorSourceState(in.State.(connector.SourceState))
		case connector.TypeDestination:
			apiConnector.State = ConnectorDestinationState(in.State.(connector.DestinationState))
		}
	}
	return apiConnector
}

func ConnectorConfig(in connector.Config) *apiv1.Connector_Config {
	return &apiv1.Connector_Config{
		Name:     in.Name,
		Settings: in.Settings,
	}
}

func ConnectorType(in connector.Type) apiv1.Connector_Type {
	return apiv1.Connector_Type(in) //nolint:gosec // this is deprecated
}

func ConnectorDestinationState(in connector.DestinationState) *apiv1.Connector_DestinationState_ {
	destinationState := &apiv1.Connector_DestinationState{Positions: map[string][]byte{}}
	for id, pos := range in.Positions {
		destinationState.Positions[id] = pos
	}
	return &apiv1.Connector_DestinationState_{
		DestinationState: destinationState,
	}
}

func ConnectorSourceState(in connector.SourceState) *apiv1.Connector_SourceState_ {
	return &apiv1.Connector_SourceState_{
		SourceState: &apiv1.Connector_SourceState{
			Position: in.Position,
		},
	}
}
