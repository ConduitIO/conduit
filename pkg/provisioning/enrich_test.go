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
					Settings: map[string]string{
						"aws.region": "us-east-1",
					},
					Processors: map[string]ProcessorConfig{
						"proc2": {
							Type:    "js",
							Workers: 2,
							Settings: map[string]string{
								"additionalProp1": "string",
							},
						},
					},
				},
			},
			Processors: map[string]ProcessorConfig{
				"proc1": {
					Type: "js",
					Settings: map[string]string{
						"additionalProp1": "string",
					},
				},
			},
		},
		"pipeline3": {
			Status:      "stopped",
			Description: "empty",
		},
	}
	after := map[string]PipelineConfig{
		"pipeline1": {
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: map[string]ConnectorConfig{
				"pipeline1:con1": {
					Type:   "source",
					Plugin: "builtin:s3",
					Name:   "pipeline1:con1",
					Settings: map[string]string{
						"aws.region": "us-east-1",
					},
					Processors: map[string]ProcessorConfig{
						"pipeline1:con1:proc2": {
							Type:    "js",
							Workers: 2,
							Settings: map[string]string{
								"additionalProp1": "string",
							},
						},
					},
				},
			},
			Processors: map[string]ProcessorConfig{
				"pipeline1:proc1": {
					Type:    "js",
					Workers: 1,
					Settings: map[string]string{
						"additionalProp1": "string",
					},
				},
			},
		},
		"pipeline3": {
			Status:      "stopped",
			Name:        "pipeline3",
			Description: "empty",
			Connectors:  map[string]ConnectorConfig{},
			Processors:  map[string]ProcessorConfig{},
		},
	}

	got := EnrichPipelinesConfig(before)
	is.Equal(after, got)
}
