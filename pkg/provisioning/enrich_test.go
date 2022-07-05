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

package provisioning

import (
	"testing"

	"github.com/matryer/is"
)

func TestEnrich_DefaultValues(t *testing.T) {
	is := is.New(t)

	before := map[string]PipelineConfig{
		"pipeline1": {
			Description: "desc1",
			Connectors: map[string]ConnectorConfig{
				"con1": {
					Type:   "source",
					Plugin: "builtin:s3",
					Name:   "",
					Settings: map[string]string{
						"aws.region": "us-east-1",
						"aws.bucket": "my-bucket",
					},
					Processors: map[string]ProcessorConfig{
						"proc1": {
							Type: "js",
							Settings: map[string]string{
								"additionalProp1": "string",
								"additionalProp2": "string",
							},
						},
					},
				},
			},
		},
		"pipeline2": {
			Status:      "stopped",
			Name:        "",
			Description: "",
			Connectors: map[string]ConnectorConfig{
				"con2": {
					Type:   "destination",
					Plugin: "builtin:file",
					Name:   "",
					Settings: map[string]string{
						"path": "my/path",
					},
					Processors: map[string]ProcessorConfig{
						"con2proc1": {
							Type: "hoistfield",
							Settings: map[string]string{
								"additionalProp1": "string",
								"additionalProp2": "string",
							},
						},
					},
				},
			},
		},
		"pipeline3": {
			Status:      "stopped",
			Name:        "",
			Description: "empty",
		},
	}
	after := map[string]PipelineConfig{
		"pipeline1": {
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: map[string]ConnectorConfig{
				"con1": {
					Type:   "source",
					Plugin: "builtin:s3",
					Name:   "con1",
					Settings: map[string]string{
						"aws.region": "us-east-1",
						"aws.bucket": "my-bucket",
					},
					Processors: map[string]ProcessorConfig{
						"proc1": {
							Type: "js",
							Settings: map[string]string{
								"additionalProp1": "string",
								"additionalProp2": "string",
							},
						},
					},
				},
			},
		},
		"pipeline2": {
			Status:      "stopped",
			Name:        "pipeline2",
			Description: "",
			Connectors: map[string]ConnectorConfig{
				"con2": {
					Type:   "destination",
					Plugin: "builtin:file",
					Name:   "con2",
					Settings: map[string]string{
						"path": "my/path",
					},
					Processors: map[string]ProcessorConfig{
						"con2proc1": {
							Type: "hoistfield",
							Settings: map[string]string{
								"additionalProp1": "string",
								"additionalProp2": "string",
							},
						},
					},
				},
			},
		},
		"pipeline3": {
			Status:      "stopped",
			Name:        "pipeline3",
			Description: "empty",
		},
	}

	got := enrichPipelinesConfig(before)
	is.Equal(after, got)
}
