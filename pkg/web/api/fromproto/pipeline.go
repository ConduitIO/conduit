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
	"github.com/conduitio/conduit/pkg/pipeline"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
)

func PipelineConfig(in *apiv1.Pipeline_Config) pipeline.Config {
	if in == nil {
		return pipeline.Config{}
	}
	return pipeline.Config{
		Name:        in.Name,
		Description: in.Description,
	}
}

func PipelineDLQ(in *apiv1.Pipeline_DLQ) pipeline.DLQ {
	if in == nil {
		return pipeline.DLQ{}
	}
	return pipeline.DLQ{
		Plugin:              in.Plugin,
		Settings:            in.Settings,
		WindowSize:          in.WindowSize,
		WindowNackThreshold: in.WindowNackThreshold,
	}
}
