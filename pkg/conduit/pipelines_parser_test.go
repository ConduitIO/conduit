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

package conduit

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
)

func Test_Parser(t *testing.T) {
	filename, err := filepath.Abs("./test/pipelines1.yml")
	if err != nil {
		t.Error(err)
	}

	want := PipelinesInfo{
		Version: "1.0",
		Pipelines: map[string]PipelineInfo{
			"pipeline1": {
				Status: "running",
				Config: PipelineConfig{"pipeline1", "desc1"},
				Processors: ProcessorInfo{
					"pipeline1proc1": {
						Name: "pipeline processor",
						Type: "transform",
						Config: ProcessorConfig{
							Settings: map[string]string{
								"additionalProp1": "string",
								"additionalProp2": "string",
							},
						},
					},
				},
				Connectors: ConnectorInfo{
					"con1": {
						Type:   "source",
						Plugin: "builtin:s3",
						Config: ConnectorConfig{
							Name: "s3-source",
							Settings: map[string]string{
								"aws.region": "us-east-1",
								"aws.bucket": "my-bucket",
							},
						},
						Processors: ProcessorInfo{
							"proc1": {
								Name: "connector processor",
								Type: "transform",
								Config: ProcessorConfig{
									Settings: map[string]string{
										"additionalProp1": "string",
										"additionalProp2": "string",
									},
								},
							},
						},
					},
				},
			},
			"pipeline2": {
				Status: "stopped",
				Config: PipelineConfig{"pipeline2", "desc2"},
				Connectors: ConnectorInfo{
					"con2": {
						Type:   "destination",
						Plugin: "builtin:file",
						Config: ConnectorConfig{
							Name: "file-dest",
							Settings: map[string]string{
								"path": "my/path",
							},
						},
						Processors: ProcessorInfo{
							"con2proc1": {
								Name: "connector processor",
								Type: "transform",
								Config: ProcessorConfig{
									Settings: map[string]string{
										"additionalProp1": "string",
										"additionalProp2": "string",
									},
								},
							},
						},
					},
				},
			},
			"pipeline3": {
				Status: "stopped",
				Config: PipelineConfig{"pipeline3", "empty"},
			},
		},
	}

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Error(err)
	}

	got, err := Parse(data)
	assert.Nil(t, err)
	assert.Equal(t, want, got)
}

func Test_Parser_Duplicate_Pipeline_Id(t *testing.T) {
	filename, err := filepath.Abs("./test/pipelines2.yml")
	if err != nil {
		t.Error(err)
	}

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Error(err)
	}

	p, err := Parse(data)
	assert.Error(t, err)
	assert.Equal(t, p, PipelinesInfo{})
}

func Test_Parser_Invalid_Version(t *testing.T) {
	filename, err := filepath.Abs("./test/pipelines3.yml")
	if err != nil {
		t.Error(err)
	}

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Error(err)
	}

	p, err := Parse(data)
	assert.Error(t, err)
	assert.Equal(t, p, PipelinesInfo{})
}

func Test_Parser_Env_Vars(t *testing.T) {
	filename, err := filepath.Abs("./test/pipelines4.yml")
	if err != nil {
		t.Error(err)
	}

	// set env variables
	err = os.Setenv("AWS_SECRET", "my-aws-secret")
	if err != nil {
		t.Fatalf("Failed to write env var: $AWS_SECRET")
	}
	err = os.Setenv("AWS_KEY", "my-aws-key")
	if err != nil {
		t.Fatalf("Failed to write env var: $AWS_KEY")
	}
	err = os.Setenv("AWS_URL", "aws-url")
	if err != nil {
		t.Fatalf("Failed to write env var: $AWS_URL")
	}

	want := PipelinesInfo{
		Version: "1.0",
		Pipelines: map[string]PipelineInfo{
			"pipeline1": {
				Status: "running",
				Config: PipelineConfig{"pipeline1", "desc1"},
				Connectors: ConnectorInfo{
					"con1": {
						Type:   "source",
						Plugin: "builtin:s3",
						Config: ConnectorConfig{
							Name: "s3-source",
							Settings: map[string]string{
								// env variables should be replaces with their values
								"aws.secret": "my-aws-secret",
								"aws.key":    "my-aws-key",
								"aws.url":    "my/aws-url/url",
							},
						},
					},
				},
			},
		},
	}

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Error(err)
	}

	got, err := Parse(data)
	assert.Nil(t, err)
	assert.Equal(t, want, got)
}
