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

package v2

import (
	"testing"

	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/google/go-cmp/cmp"
)

func TestConfiguration_FromConfig(t *testing.T) {
	pipelines := testPipelineConfigs()
	expected := expectedModelConfiguration()

	c := FromConfig(pipelines)

	if diff := cmp.Diff(c, expected); diff != "" {
		t.Logf("mismatch (-want +got): %s", diff)
		t.Fail()
	}
}

func testPipelineConfigs() []config.Pipeline {
	uint64Ptr := func(i uint64) *uint64 { return &i }

	return []config.Pipeline{
		{
			ID:          "pipeline1",
			Name:        "pipeline1",
			Status:      "running",
			Description: "desc1",
			Connectors: []config.Connector{
				{
					ID:     "con1",
					Type:   "source",
					Plugin: "builtin:s3",
					Name:   "s3-source",
					Settings: map[string]string{
						"aws.region": "us-east-1",
						"aws.bucket": "my-bucket",
					},
					Processors: []config.Processor{
						{
							ID:     "proc1",
							Plugin: "js",
							Settings: map[string]string{
								"additionalProp1": "string",
								"additionalProp2": "string",
							},
							Workers: 1,
						},
					},
				},
				{
					ID:     "con2",
					Type:   "destination",
					Plugin: "builtin:log",
					Name:   "log-destination",
				},
			},
			Processors: []config.Processor{
				{
					ID:     "pipeline1proc1",
					Plugin: "js",
					Settings: map[string]string{
						"additionalProp1": "string",
						"additionalProp2": "string",
					},
					Workers: 1,
				},
			},
			DLQ: config.DLQ{
				Plugin: "builtin:log",
				Settings: map[string]string{
					"level":   "error",
					"message": "record delivery failed",
				},
				WindowSize:          uint64Ptr(4),
				WindowNackThreshold: uint64Ptr(2),
			},
		},
		{
			ID:          "pipeline2",
			Name:        "pipeline2",
			Status:      "stopped",
			Description: "desc2",
			Connectors: []config.Connector{
				{
					ID:     "con2",
					Type:   "destination",
					Plugin: "builtin:file",
					Name:   "file-dest",
					Settings: map[string]string{
						"path": "my/path",
					},
					Processors: []config.Processor{
						{
							ID:     "con2proc1",
							Plugin: "hoistfield",
							Settings: map[string]string{
								"additionalProp1": "string",
								"additionalProp2": "string",
							},
							Workers: 1,
						},
					},
				},
			},
		},
	}
}

func expectedModelConfiguration() Configuration {
	uint64Ptr := func(i uint64) *uint64 { return &i }

	return Configuration{
		Version: "2.2",
		Pipelines: []Pipeline{
			{
				ID:          "pipeline1",
				Status:      "running",
				Name:        "pipeline1",
				Description: "desc1",
				Connectors: []Connector{
					{
						ID:     "con1",
						Type:   "source",
						Plugin: "builtin:s3",
						Name:   "s3-source",
						Settings: map[string]string{
							"aws.bucket": "my-bucket",
							"aws.region": "us-east-1",
						},
						Processors: []Processor{
							{
								ID:     "proc1",
								Plugin: "js",
								Settings: map[string]string{
									"additionalProp1": "string",
									"additionalProp2": "string",
								},
								Workers: 1,
							},
						},
					},
					{
						ID:         "con2",
						Type:       "destination",
						Plugin:     "builtin:log",
						Name:       "log-destination",
						Processors: []Processor{},
					},
				},
				Processors: []Processor{
					{
						ID:     "pipeline1proc1",
						Plugin: "js",
						Settings: map[string]string{
							"additionalProp1": "string",
							"additionalProp2": "string",
						},
						Workers: 1,
					},
				},
				DLQ: DLQ{
					Plugin: "builtin:log",
					Settings: map[string]string{
						"level":   "error",
						"message": "record delivery failed",
					},
					WindowSize:          uint64Ptr(4),
					WindowNackThreshold: uint64Ptr(2),
				},
			},
			{
				ID:          "pipeline2",
				Status:      "stopped",
				Name:        "pipeline2",
				Description: "desc2",
				Connectors: []Connector{
					{
						ID:     "con2",
						Type:   "destination",
						Plugin: "builtin:file",
						Name:   "file-dest",
						Settings: map[string]string{
							"path": "my/path",
						},
						Processors: []Processor{
							{
								ID:     "con2proc1",
								Plugin: "hoistfield",
								Settings: map[string]string{
									"additionalProp1": "string",
									"additionalProp2": "string",
								},
								Workers: 1,
							},
						},
					},
				},
				Processors: []Processor{},
				DLQ:        DLQ{},
			},
		},
	}
}
