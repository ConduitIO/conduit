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

package yaml

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/log"
	v1 "github.com/conduitio/conduit/pkg/provisioning/config/yaml/v1"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func TestParser_V1_Success(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v1/testdata/pipelines1-success.yml"
	intPtr := func(i int) *int { return &i }
	want := []Configuration{
		v1.Configuration{
			Version: "1.0",
			Pipelines: map[string]v1.Pipeline{
				"pipeline1": {
					Status:      "running",
					Name:        "pipeline1",
					Description: "desc1",
					Processors: map[string]v1.Processor{
						"pipeline1proc1": {
							Type: "js",
							Settings: map[string]string{
								"additionalProp1": "string",
								"additionalProp2": "string",
							},
						},
					},
					Connectors: map[string]v1.Connector{
						"con1": {
							Type:   "source",
							Plugin: "builtin:s3",
							Name:   "s3-source",
							Settings: map[string]string{
								"aws.region": "us-east-1",
								"aws.bucket": "my-bucket",
							},
							Processors: map[string]v1.Processor{
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
					DLQ: v1.DLQ{
						Plugin: "my-plugin",
						Settings: map[string]string{
							"foo": "bar",
						},
						WindowSize:          intPtr(4),
						WindowNackThreshold: intPtr(2),
					},
				},
			},
		},
		v1.Configuration{
			Version: "1.1",
			Pipelines: map[string]v1.Pipeline{
				"pipeline2": {
					Status:      "stopped",
					Name:        "pipeline2",
					Description: "desc2",
					Connectors: map[string]v1.Connector{
						"con2": {
							Type:   "destination",
							Plugin: "builtin:file",
							Name:   "file-dest",
							Settings: map[string]string{
								"path": "my/path",
							},
							Processors: map[string]v1.Processor{
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
			},
		},
	}

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	got, err := parser.ParseConfigurations(context.Background(), file)
	is.NoErr(err)
	is.Equal(got, want)
}

func TestParser_V1_Warnings(t *testing.T) {
	is := is.New(t)
	var out bytes.Buffer
	logger := log.New(zerolog.New(&out))
	parser := NewParser(logger)

	filepath := "./v1/testdata/pipelines1-success.yml"
	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	_, err = parser.ParseConfigurations(context.Background(), file)
	is.NoErr(err)

	// check warnings
	want := `{"level":"warn","component":"yaml.Parser","line":5,"column":5,"field":"unknownField","message":"field unknownField not found in type v1.Pipeline"}
{"level":"warn","component":"yaml.Parser","line":18,"column":11,"field":"processors","message":"the order of processors is non-deterministic in configuration files with version 1.x, please upgrade to version 2.x"}
{"level":"warn","component":"yaml.Parser","line":24,"column":7,"field":"processors","message":"the order of processors is non-deterministic in configuration files with version 1.x, please upgrade to version 2.x"}
{"level":"warn","component":"yaml.Parser","line":31,"column":7,"field":"dead-letter-queue","message":"field dead-letter-queue was introduced in version 1.1, please update the pipeline config version"}
{"level":"warn","component":"yaml.Parser","line":52,"column":11,"field":"processors","message":"the order of processors is non-deterministic in configuration files with version 1.x, please upgrade to version 2.x"}
`
	is.Equal(out.String(), want)
}

func TestParser_V1_DuplicatePipelineId(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v1/testdata/pipelines2-duplicate-pipeline-id.yml"

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	_, err = parser.ParseConfigurations(context.Background(), file)
	is.NoErr(err)
}

func TestParser_V1_EmptyFile(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v1/testdata/pipelines3-empty.yml"

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	_, err = parser.ParseConfigurations(context.Background(), file)
	is.NoErr(err)
}

func TestParser_V1_InvalidYaml(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v1/testdata/pipelines4-invalid-yaml.yml"

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	_, err = parser.ParseConfigurations(context.Background(), file)
	is.True(err != nil)
}

func TestParser_V1_EnvVars(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v1/testdata/pipelines5-env-vars.yml"

	// set env variables
	err := os.Setenv("TEST_PARSER_AWS_SECRET", "my-aws-secret")
	if err != nil {
		t.Fatalf("Failed to write env var: $TEST_PARSER_AWS_SECRET")
	}
	err = os.Setenv("TEST_PARSER_AWS_KEY", "my-aws-key")
	if err != nil {
		t.Fatalf("Failed to write env var: $TEST_PARSER_AWS_KEY")
	}
	err = os.Setenv("TEST_PARSER_AWS_URL", "aws-url")
	if err != nil {
		t.Fatalf("Failed to write env var: $TEST_PARSER_AWS_URL")
	}

	want := []Configuration{
		v1.Configuration{
			Version: "1.0",
			Pipelines: map[string]v1.Pipeline{
				"pipeline1": {
					Status:      "running",
					Name:        "pipeline1",
					Description: "desc1",
					Connectors: map[string]v1.Connector{
						"con1": {
							Type:   "source",
							Plugin: "builtin:s3",
							Name:   "s3-source",
							Settings: map[string]string{
								// env variables should be replaced with their values
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

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	got, err := parser.ParseConfigurations(context.Background(), file)
	is.NoErr(err)
	is.Equal(got, want)
}
