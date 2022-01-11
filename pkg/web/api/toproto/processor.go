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
)

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var procTypes [1]struct{}
	_ = procTypes[int(processor.TypeTransform)-int(apiv1.Processor_TYPE_TRANSFORM)]
	_ = procTypes[int(processor.TypeFilter)-int(apiv1.Processor_TYPE_FILTER)]

	var parentTypes [1]struct{}
	_ = parentTypes[int(processor.ParentTypeConnector)-int(apiv1.Processor_Parent_TYPE_CONNECTOR)]
	_ = parentTypes[int(processor.ParentTypePipeline)-int(apiv1.Processor_Parent_TYPE_PIPELINE)]
}

func Processor(in *processor.Instance) *apiv1.Processor {
	return &apiv1.Processor{
		Id:     in.ID,
		Name:   in.Name,
		Type:   ProcessorType(in.Processor.Type()),
		Config: ProcessorConfig(in.Config),
		Parent: ProcessorParent(in.Parent),
	}
}

func ProcessorConfig(in processor.Config) *apiv1.Processor_Config {
	return &apiv1.Processor_Config{
		Settings: in.Settings,
	}
}

func ProcessorType(in processor.Type) apiv1.Processor_Type {
	return apiv1.Processor_Type(in)
}

func ProcessorParent(in processor.Parent) *apiv1.Processor_Parent {
	return &apiv1.Processor_Parent{
		Id:   in.ID,
		Type: apiv1.Processor_Parent_Type(in.Type),
	}
}
