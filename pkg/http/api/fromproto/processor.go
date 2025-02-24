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

package fromproto

import (
	"github.com/conduitio/conduit/pkg/processor"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
)

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var parentTypes [1]struct{}
	_ = parentTypes[int(processor.ParentTypeConnector)-int(apiv1.Processor_Parent_TYPE_CONNECTOR)]
	_ = parentTypes[int(processor.ParentTypePipeline)-int(apiv1.Processor_Parent_TYPE_PIPELINE)]
}

func ProcessorConfig(in *apiv1.Processor_Config) processor.Config {
	if in == nil {
		return processor.Config{}
	}
	return processor.Config{
		Settings: in.Settings,
		Workers:  int(in.Workers),
	}
}

func ProcessorParent(in *apiv1.Processor_Parent) processor.Parent {
	if in == nil {
		return processor.Parent{}
	}
	return processor.Parent{
		ID:   in.Id,
		Type: processor.ParentType(in.Type),
	}
}
