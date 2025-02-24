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

package pipelines4

import (
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
)

// ---------------
// -- pipeline1 --
// ---------------

var (
	P1     *pipeline.Instance
	P1C1   *connector.Instance
	P1C2   *connector.Instance
	P1P1   *processor.Instance
	P1C2P1 *processor.Instance
	P2     *pipeline.Instance
	P2C1   *connector.Instance
	P3     *pipeline.Instance
)

func init() {
	P1 = &pipeline.Instance{
		ID: "pipeline1",
		Config: pipeline.Config{
			Name:        "name1",
			Description: "desc1",
		},
		Error:        "",
		DLQ:          pipeline.DefaultDLQ,
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
			Name:     "file-src",
			Settings: map[string]string{"path": "./test/source-file.txt"},
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
			Name:     "file-dest",
			Settings: map[string]string{"path": "./test/dest-file.txt"},
		},
		PipelineID:   "pipeline1",
		Plugin:       "builtin:file",
		ProcessorIDs: []string{"pipeline1:con2:con2proc1"},

		ProvisionedBy: connector.ProvisionTypeConfig,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	P1P1 = &processor.Instance{
		ID:     "pipeline1:proc1",
		Plugin: "builtin:field.exclude",
		Parent: processor.Parent{
			ID:   "pipeline1",
			Type: processor.ParentTypePipeline,
		},
		Config: processor.Config{
			Settings: map[string]string{
				"fields": `.Metadata["opencdc.readAt"]`,
			},
			Workers: 1,
		},

		ProvisionedBy: processor.ProvisionTypeConfig,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	P1C2P1 = &processor.Instance{
		ID:     "pipeline1:con2:con2proc1",
		Plugin: "builtin:field.exclude",
		Parent: processor.Parent{
			ID:   "pipeline1:con2",
			Type: processor.ParentTypeConnector,
		},
		Config: processor.Config{
			Settings: map[string]string{
				"fields": `.Metadata["opencdc.readAt"]`,
			},
			Workers: 1,
		},

		ProvisionedBy: processor.ProvisionTypeConfig,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	// ---------------
	// -- pipeline2 --
	// ---------------

	P2 = &pipeline.Instance{
		ID: "pipeline2",
		Config: pipeline.Config{
			Name:        "name2",
			Description: "desc2",
		},
		Error:        "",
		DLQ:          pipeline.DefaultDLQ,
		ConnectorIDs: []string{"pipeline2:con1"},
		ProcessorIDs: nil,

		ProvisionedBy: pipeline.ProvisionTypeConfig,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	P2.SetStatus(pipeline.StatusUserStopped)

	P2C1 = &connector.Instance{
		ID:   "pipeline2:con1",
		Type: connector.TypeDestination,
		Config: connector.Config{
			Name:     "file-dest",
			Settings: map[string]string{"path": "./test/file3.txt"},
		},
		PipelineID:   "pipeline2",
		Plugin:       "builtin:file",
		ProcessorIDs: nil,

		ProvisionedBy: connector.ProvisionTypeConfig,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	// ---------------
	// -- pipeline3 --
	// ---------------

	P3 = &pipeline.Instance{
		ID: "pipeline3",
		Config: pipeline.Config{
			Name:        "name3",
			Description: "empty",
		},
		Error:        "",
		DLQ:          pipeline.DefaultDLQ,
		ConnectorIDs: nil,
		ProcessorIDs: nil,

		ProvisionedBy: pipeline.ProvisionTypeConfig,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	P3.SetStatus(pipeline.StatusUserStopped)
}
