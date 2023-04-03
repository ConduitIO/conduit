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

package pipelines3

import (
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/pipeline"
)

var P1 = &pipeline.Instance{
	ID: "pipeline1",
	Config: pipeline.Config{
		Name:        "name1",
		Description: "desc1",
	},
	Status:       pipeline.StatusRunning,
	Error:        "",
	DLQ:          pipeline.DefaultDLQ,
	ConnectorIDs: []string{"pipeline1:con1", "pipeline1:con2"},
	ProcessorIDs: nil,

	ProvisionedBy: pipeline.ProvisionTypeConfig,
	CreatedAt:     time.Now(),
	UpdatedAt:     time.Now(),
}

var P1C1 = &connector.Instance{
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

var P1C2 = &connector.Instance{
	ID:   "pipeline1:con2",
	Type: connector.TypeDestination,
	Config: connector.Config{
		Name:     "dest",
		Settings: map[string]string{"path": "my/path/file2.txt"},
	},
	PipelineID:   "pipeline1",
	Plugin:       "builtin:file",
	ProcessorIDs: nil,

	ProvisionedBy: connector.ProvisionTypeConfig,
	CreatedAt:     time.Now(),
	UpdatedAt:     time.Now(),
}
