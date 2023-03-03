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
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func TestParser_Success(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./test/pipelines1-success.yml"
	intPtr := func(i int) *int { return &i }
	want := map[string]Pipeline{
		"pipeline1": {
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Processors: map[string]Processor{
				"pipeline1proc1": {
					Type: "js",
					Settings: map[string]string{
						"additionalProp1": "string",
						"additionalProp2": "string",
					},
				},
			},
			Connectors: map[string]Connector{
				"con1": {
					Type:   "source",
					Plugin: "builtin:s3",
					Name:   "s3-source",
					Settings: map[string]string{
						"aws.region": "us-east-1",
						"aws.bucket": "my-bucket",
					},
					Processors: map[string]Processor{
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
			DLQ: DLQ{
				Plugin: "my-plugin",
				Settings: map[string]string{
					"foo": "bar",
				},
				WindowSize:          intPtr(4),
				WindowNackThreshold: intPtr(2),
			},
		},
		"pipeline2": {
			Status:      "stopped",
			Name:        "pipeline2",
			Description: "desc2",
			Connectors: map[string]Connector{
				"con2": {
					Type:   "destination",
					Plugin: "builtin:file",
					Name:   "file-dest",
					Settings: map[string]string{
						"path": "my/path",
					},
					Processors: map[string]Processor{
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

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	got, err := parser.ParseConfiguration(context.Background(), file)
	is.NoErr(err)
	is.Equal(want, got.Pipelines)
}

func TestParser_Warnings(t *testing.T) {
	is := is.New(t)
	var out bytes.Buffer
	logger := log.New(zerolog.New(&out))
	parser := NewParser(logger)

	filepath := "./test/pipelines1-success.yml"
	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	_, err = parser.ParseConfiguration(context.Background(), file)
	is.NoErr(err)

	// check warnings
	want := `{"level":"warn","component":"yaml.Parser","line":5,"column":5,"message":"field unknownField not found in type yaml.Pipeline"}
{"level":"warn","component":"yaml.Parser","line":31,"column":15,"field":"dead-letter-queue","value":"my-plugin","message":"field dead-letter-queue was introduced in version 1.1, please update the pipeline config version"}
{"level":"warn","component":"yaml.Parser","line":33,"column":14,"field":"dead-letter-queue","value":"bar","message":"field dead-letter-queue was introduced in version 1.1, please update the pipeline config version"}
{"level":"warn","component":"yaml.Parser","line":34,"column":20,"field":"dead-letter-queue","value":"4","message":"field dead-letter-queue was introduced in version 1.1, please update the pipeline config version"}
{"level":"warn","component":"yaml.Parser","line":35,"column":30,"field":"dead-letter-queue","value":"2","message":"field dead-letter-queue was introduced in version 1.1, please update the pipeline config version"}
`
	is.Equal(want, out.String())
}

func TestParser_DuplicatePipelineId(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./test/pipelines2-duplicate-pipeline-id.yml"

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	_, err = parser.ParseConfiguration(context.Background(), file)
	is.True(err != nil)
}

func TestParser_EmptyFile(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./test/pipelines5-empty.yml"

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	_, err = parser.ParseConfiguration(context.Background(), file)
	is.NoErr(err)
}

func TestParser_InvalidYaml(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./test/pipelines6-invalid-yaml.yml"

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	_, err = parser.ParseConfiguration(context.Background(), file)
	is.True(err != nil)
}

func TestParser_EnvVars(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./test/pipelines7-env-vars.yml"

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

	want := map[string]Pipeline{
		"pipeline1": {
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: map[string]Connector{
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
	}

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	got, err := parser.ParseConfiguration(context.Background(), file)
	is.NoErr(err)
	is.Equal(want, got.Pipelines)
}
