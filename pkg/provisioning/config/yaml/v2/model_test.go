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
	uint64Ptr := func(i uint64) *uint64 { return &i }
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
			WindowSize:          uint64Ptr(4),
			WindowNackThreshold: uint64Ptr(2),
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
