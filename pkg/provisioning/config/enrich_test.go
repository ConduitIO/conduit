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

package config

import (
	"testing"

	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/matryer/is"
)

func TestEnrich_DefaultValues(t *testing.T) {
	is := is.New(t)

	testCases := []struct {
		name string
		have Pipeline
		want Pipeline
	}{{
		name: "pipeline1",
		have: Pipeline{
			ID:          "pipeline1",
			Description: "desc1",
			Connectors: []Connector{
				{
					ID:     "con1",
					Type:   "source",
					Plugin: "builtin:s3",
					Settings: map[string]string{
						"aws.region": "us-east-1",
					},
					Processors: []Processor{
						{
							ID:      "proc2",
							Type:    "js",
							Workers: 2,
							Settings: map[string]string{
								"additionalProp1": "string",
							},
						},
					},
				},
			},
			Processors: []Processor{
				{
					ID:   "proc1",
					Type: "js",
					Settings: map[string]string{
						"additionalProp1": "string",
					},
				},
			},
		},
		want: Pipeline{
			ID:          "pipeline1",
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			DLQ: DLQ{
				Plugin:              pipeline.DefaultDLQ.Plugin,
				Settings:            pipeline.DefaultDLQ.Settings,
				WindowSize:          &pipeline.DefaultDLQ.WindowSize,
				WindowNackThreshold: &pipeline.DefaultDLQ.WindowNackThreshold,
			},
			Connectors: []Connector{
				{
					ID:     "pipeline1:con1",
					Type:   "source",
					Plugin: "builtin:s3",
					Name:   "con1",
					Settings: map[string]string{
						"aws.region": "us-east-1",
					},
					Processors: []Processor{
						{
							ID:      "pipeline1:con1:proc2",
							Type:    "js",
							Workers: 2,
							Settings: map[string]string{
								"additionalProp1": "string",
							},
						},
					},
				},
			},
			Processors: []Processor{
				{
					ID:      "pipeline1:proc1",
					Type:    "js",
					Workers: 1,
					Settings: map[string]string{
						"additionalProp1": "string",
					},
				},
			},
		},
	}, {
		name: "pipeline2",
		have: Pipeline{
			ID:          "pipeline2",
			Status:      "stopped",
			Description: "empty",
		},
		want: Pipeline{
			ID:          "pipeline2",
			Status:      "stopped",
			Name:        "pipeline2",
			Description: "empty",
			DLQ: DLQ{
				Plugin:              pipeline.DefaultDLQ.Plugin,
				Settings:            pipeline.DefaultDLQ.Settings,
				WindowSize:          &pipeline.DefaultDLQ.WindowSize,
				WindowNackThreshold: &pipeline.DefaultDLQ.WindowNackThreshold,
			},
			Connectors: []Connector{},
			Processors: []Processor{},
		},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := Enrich(tc.have)
			is.Equal(got, tc.want)
		})
	}
}
