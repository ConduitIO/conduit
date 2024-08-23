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
	"github.com/conduitio/conduit/pkg/processor"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var parentTypes [1]struct{}
	_ = parentTypes[int(processor.ParentTypeConnector)-int(apiv1.Processor_Parent_TYPE_CONNECTOR)]
	_ = parentTypes[int(processor.ParentTypePipeline)-int(apiv1.Processor_Parent_TYPE_PIPELINE)]
}

func Processor(in *processor.Instance) *apiv1.Processor {
	return &apiv1.Processor{
		Id:        in.ID,
		Plugin:    in.Plugin,
		CreatedAt: timestamppb.New(in.CreatedAt),
		UpdatedAt: timestamppb.New(in.UpdatedAt),
		Config:    ProcessorConfig(in.Config),
		Parent:    ProcessorParent(in.Parent),
		Condition: in.Condition,
	}
}

func ProcessorConfig(in processor.Config) *apiv1.Processor_Config {
	return &apiv1.Processor_Config{
		Settings: in.Settings,
		Workers:  in.Workers,
	}
}

func ProcessorParent(in processor.Parent) *apiv1.Processor_Parent {
	return &apiv1.Processor_Parent{
		Id:   in.ID,
		Type: apiv1.Processor_Parent_Type(in.Type),
	}
}
