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

	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

func TestConfiguration_JSON(t *testing.T) {
	is := is.New(t)
	intPtr := func(i int) *int { return &i }
	p := Pipeline{
		ID:          "pipeline1",
		Status:      "running",
		Name:        "pipeline1",
		Description: "desc1",
		Processors: []Processor{
			{
				ID:        "pipeline1proc1",
				Plugin:    "alpha",
				Condition: "{{ true }}",
				Settings: map[string]string{
					"additionalProp1": "string",
					"additionalProp2": "string",
				},
			},
		},
		Connectors: []Connector{
			{
				ID:     "con1",
				Type:   "source",
				Plugin: "builtin:s3",
				Name:   "s3-source",
				Settings: map[string]string{
					"aws.region": "us-east-1",
					"aws.bucket": "my-bucket",
				},
				Processors: []Processor{
					{
						ID:   "proc1",
						Type: "js",
						Settings: map[string]string{
							"additionalProp1": "string",
							"additionalProp2": "string",
						},
					},
				},
			},
		},
		DLQ: DLQ{
			Plugin: "my-plugin",
			Settings: map[string]string{
				"foo": "bar",
			},
			WindowSize:          intPtr(4),
			WindowNackThreshold: intPtr(2),
		},
	}

	want := `{
  "id": "pipeline1",
  "status": "running",
  "name": "pipeline1",
  "description": "desc1",
  "connectors": [
    {
      "id": "con1",
      "type": "source",
      "plugin": "builtin:s3",
      "name": "s3-source",
      "settings": {
        "aws.bucket": "my-bucket",
        "aws.region": "us-east-1"
      },
      "processors": [
        {
          "id": "proc1",
          "type": "js",
          "plugin": "",
          "condition": "",
          "settings": {
            "additionalProp1": "string",
            "additionalProp2": "string"
          },
          "workers": 0
        }
      ]
    }
  ],
  "processors": [
    {
      "id": "pipeline1proc1",
      "type": "",
      "plugin": "alpha",
      "condition": "{{ true }}",
      "settings": {
        "additionalProp1": "string",
        "additionalProp2": "string"
      },
      "workers": 0
    }
  ],
  "dead-letter-queue": {
    "plugin": "my-plugin",
    "settings": {
      "foo": "bar"
    },
    "window-size": 4,
    "window-nack-threshold": 2
  }
}`
	got, err := json.MarshalIndent(p, "", "  ")
	is.NoErr(err)

	is.Equal(string(got), want)
}

// TestToConfig_EmptySettingsNormalizedToNil is a regression test for a bug
// the root github.com/conduitio/conduit package's pipeline-builder
// round-trip property test caught: Connector/Processor/DLQ.ToConfig used to
// copy Settings as-is, so a non-nil-but-empty map (produced whenever
// FromConfig's output is marshaled to YAML and reparsed — yaml.v3 has no
// way to marshal a nil map as an omitted key without an explicit omitempty
// tag, which this wire format has never carried on Settings) round-tripped
// into config.{Connector,Processor,DLQ}.Settings as map[string]string{}
// instead of nil, even though the caller-visible input (either a truly
// nil-Settings config.Pipeline, or a YAML document that omits the settings
// key) never had explicit empty-map intent. ToConfig now normalizes an
// empty Settings map to nil, mirroring the empty-slice-to-nil
// normalization connectorsToConfig/processorsToConfig already apply.
func TestToConfig_EmptySettingsNormalizedToNil(t *testing.T) {
	is := is.New(t)

	c := Connector{ID: "con1", Type: "source", Plugin: "builtin:s3", Settings: map[string]string{}}
	is.True(c.ToConfig().Settings == nil)

	p := Processor{ID: "proc1", Plugin: "js", Settings: map[string]string{}}
	is.True(p.ToConfig().Settings == nil)

	d := DLQ{Plugin: "builtin:log", Settings: map[string]string{}}
	is.True(d.ToConfig().Settings == nil)
}
