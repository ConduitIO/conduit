// Copyright © 2023 Meroxa, Inc.
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

package pipelines1

import (
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
)

var (
	P1     *pipeline.Instance
	P1C1   *connector.Instance
	P1C2   *connector.Instance
	P1P1   *processor.Instance
	P1C2P1 *processor.Instance
)

func init() {
	P1 = &pipeline.Instance{
		ID: "pipeline1",
		Config: pipeline.Config{
			Name:        "name1",
			Description: "desc1",
		},
		Error: "",
		DLQ: pipeline.DLQ{
			Plugin:              pipeline.DefaultDLQ.Plugin,
			Settings:            pipeline.DefaultDLQ.Settings,
			WindowSize:          20,
			WindowNackThreshold: 10,
		},
		ConnectorIDs: []string{"pipeline1:con1", "pipeline1:con2"},
		ProcessorIDs: []string{"pipeline1:proc1"},

		ProvisionedBy: pipeline.ProvisionTypeConfig,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	P1.SetStatus(pipeline.StatusRunning)

	P1C1 = &connector.Instance{
		ID:   "pipeline1:con1",
		Type: connector.TypeSource,
		Config: connector.Config{
			Name:     "source",
			Settings: map[string]string{"path": "my/path/file1.txt"},
		},
		PipelineID:   "pipeline1",
		Plugin:       "builtin:file",
		ProcessorIDs: nil,

		ProvisionedBy: connector.ProvisionTypeConfig,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	P1C2 = &connector.Instance{
		ID:   "pipeline1:con2",
		Type: connector.TypeDestination,
		Config: connector.Config{
			Name:     "dest",
			Settings: map[string]string{"path": "my/path/file2.txt"},
		},
		PipelineID:   "pipeline1",
		Plugin:       "builtin:file",
		ProcessorIDs: []string{"pipeline1:con2:proc1con"},

		ProvisionedBy: connector.ProvisionTypeConfig,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	P1P1 = &processor.Instance{
		ID:     "pipeline1:proc1",
		Plugin: "js",
		Parent: processor.Parent{
			ID:   "pipeline1",
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Workers:  1,
			Settings: map[string]string{"additionalProp1": "string"},
		},

		ProvisionedBy: processor.ProvisionTypeConfig,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	P1C2P1 = &processor.Instance{
		ID:     "pipeline1:con2:proc1con",
		Plugin: "js",
		Parent: processor.Parent{
			ID:   "pipeline1:con2",
			Type: processor.ParentTypeConnector,
		},
		Config: processor.Config{
			Workers:  10,
			Settings: map[string]string{"additionalProp1": "string"},
		},

		ProvisionedBy: processor.ProvisionTypeConfig,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
}
