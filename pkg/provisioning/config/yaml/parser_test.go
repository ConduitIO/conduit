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
	"io"
	"os"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	v1 "github.com/conduitio/conduit/pkg/provisioning/config/yaml/v1"
	v2 "github.com/conduitio/conduit/pkg/provisioning/config/yaml/v2"
	"github.com/conduitio/yaml/v3"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func TestParser_V1_Success(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v1/testdata/pipelines1-success.yml"

	uint64Ptr := func(i uint64) *uint64 { return &i }
	want := Configurations{
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
						WindowSize:          uint64Ptr(4),
						WindowNackThreshold: uint64Ptr(2),
					},
				},
			},
		},
		v1.Configuration{
			Version: "1.12",
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
{"level":"warn","component":"yaml.Parser","line":17,"column":9,"field":"processors","message":"the order of processors is non-deterministic in configuration files with version 1.x, please upgrade to version 2.x"}
{"level":"warn","component":"yaml.Parser","line":23,"column":5,"field":"processors","message":"the order of processors is non-deterministic in configuration files with version 1.x, please upgrade to version 2.x"}
{"level":"warn","component":"yaml.Parser","line":30,"column":5,"field":"dead-letter-queue","message":"field dead-letter-queue was introduced in version 1.1, please update the pipeline config version"}
{"level":"warn","component":"yaml.Parser","line":38,"column":1,"field":"version","value":"1.12","message":"unrecognized version 1.12, falling back to parser version 1.1"}
{"level":"warn","component":"yaml.Parser","line":51,"column":9,"field":"processors","message":"the order of processors is non-deterministic in configuration files with version 1.x, please upgrade to version 2.x"}
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

	want := Configurations{
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

func TestParser_V1_ParseV2Config(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v2/testdata/pipelines1-success.yml"

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	// replace major version so that the v1 parser is chosen for a v2 config
	r := replacingReader{
		Reader: file,
		Old:    []byte("version: 2"),
		New:    []byte("version: 1"),
	}

	_, err = parser.ParseConfigurations(context.Background(), r)
	is.True(err != nil)
	// make sure it's an invalid type error
	var iterr *yaml.InvalidTypeError
	is.True(cerrors.As(err, &iterr))
}

func TestParser_V2_Success(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v2/testdata/pipelines1-success.yml"

	uint64Ptr := func(i uint64) *uint64 { return &i }
	want := Configurations{
		v2.Configuration{
			Version: "2.2",
			Pipelines: []v2.Pipeline{
				{
					ID:          "pipeline1",
					Status:      "running",
					Name:        "pipeline1",
					Description: "desc1",
					Processors: []v2.Processor{
						{
							ID:     "pipeline1proc1",
							Plugin: "js",
							Settings: map[string]string{
								"additionalProp1": "string",
								"additionalProp2": "string",
							},
						},
					},
					Connectors: []v2.Connector{
						{
							ID:     "con1",
							Type:   "source",
							Plugin: "builtin:s3",
							Name:   "s3-source",
							Settings: map[string]string{
								"aws.region": "us-east-1",
								"aws.bucket": "my-bucket",
							},
							Processors: []v2.Processor{
								{
									ID:     "proc1",
									Plugin: "js",
									Settings: map[string]string{
										"additionalProp1": "string",
										"additionalProp2": "string",
									},
								},
							},
						},
					},
					DLQ: v2.DLQ{
						Plugin: "my-plugin",
						Settings: map[string]string{
							"foo": "bar",
						},
						WindowSize:          uint64Ptr(4),
						WindowNackThreshold: uint64Ptr(2),
					},
				},
			},
		},
		v2.Configuration{
			Version: "2.12",
			Pipelines: []v2.Pipeline{
				{
					ID:          "pipeline2",
					Status:      "stopped",
					Name:        "pipeline2",
					Description: "desc2",
					Connectors: []v2.Connector{
						{
							ID:     "con2",
							Type:   "destination",
							Plugin: "builtin:file",
							Name:   "file-dest",
							Settings: map[string]string{
								"path": "my/path",
							},
							Processors: []v2.Processor{
								{
									ID:     "con2proc1",
									Plugin: "hoistfield",
									Settings: map[string]string{
										"additionalProp1": "string",
										"additionalProp2": "string",
									},
								},
							},
						},
					},
				},
				{
					ID:          "pipeline3",
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
	diff := cmp.Diff(want, got)
	if diff != "" {
		t.Errorf("%v", diff)
	}
}

func TestParser_V2_BackwardsCompatibility(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v2/testdata/pipelines6-bwc.yml"
	want := Configurations{
		v2.Configuration{
			Version: "2.2",
			Pipelines: []v2.Pipeline{
				{
					ID:          "pipeline6",
					Status:      "running",
					Name:        "pipeline6",
					Description: "desc1",
					Processors: []v2.Processor{
						{
							ID:   "pipeline1proc1",
							Type: "js",
							Settings: map[string]string{
								"additionalProp1": "string",
								"additionalProp2": "string",
							},
						},
					},
					Connectors: []v2.Connector{
						{
							ID:     "con1",
							Type:   "source",
							Plugin: "builtin:s3",
							Name:   "s3-source",
							Settings: map[string]string{
								"aws.region": "us-east-1",
								"aws.bucket": "my-bucket",
							},
							Processors: []v2.Processor{
								{
									ID:     "proc1",
									Plugin: "js",
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
		},
	}

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	got, err := parser.ParseConfigurations(context.Background(), file)
	is.NoErr(err)
	diff := cmp.Diff(want, got)
	if diff != "" {
		t.Errorf("%v", diff)
	}
}

func TestParser_V2_Warnings(t *testing.T) {
	is := is.New(t)
	var out bytes.Buffer
	logger := log.New(zerolog.New(&out))
	parser := NewParser(logger)

	filepath := "./v2/testdata/pipelines1-success.yml"
	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	_, err = parser.ParseConfigurations(context.Background(), file)
	is.NoErr(err)

	// check warnings
	want := `{"level":"warn","component":"yaml.Parser","line":6,"column":5,"field":"unknownField","message":"field unknownField not found in type v2.Pipeline"}
{"level":"warn","component":"yaml.Parser","line":38,"column":1,"field":"version","value":"2.12","message":"unrecognized version 2.12, falling back to parser version 2.2"}
`
	is.Equal(out.String(), want)
}

func TestParser_V2_DuplicatePipelineId(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v2/testdata/pipelines2-duplicate-pipeline-id.yml"

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	_, err = parser.ParseConfigurations(context.Background(), file)
	is.NoErr(err)
}

func TestParser_V2_EmptyFile(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v2/testdata/pipelines3-empty.yml"

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	_, err = parser.ParseConfigurations(context.Background(), file)
	is.NoErr(err)
}

func TestParser_V2_InvalidYaml(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v2/testdata/pipelines4-invalid-yaml.yml"

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	_, err = parser.ParseConfigurations(context.Background(), file)
	is.True(err != nil)
}

func TestParser_V2_EnvVars(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v2/testdata/pipelines5-env-vars.yml"

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

	want := Configurations{
		v2.Configuration{
			Version: "2.0",
			Pipelines: []v2.Pipeline{
				{
					ID:          "pipeline1",
					Status:      "running",
					Name:        "pipeline1",
					Description: "desc1",
					Connectors: []v2.Connector{
						{
							ID:     "con1",
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

func TestParser_V2_ParseV1Config(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v1/testdata/pipelines1-success.yml"

	file, err := os.Open(filepath)
	is.NoErr(err)
	defer file.Close()

	// replace major version so that the v2 parser is chosen for a v1 config
	r := replacingReader{
		Reader: file,
		Old:    []byte("version: 1"),
		New:    []byte("version: 2"),
	}

	_, err = parser.ParseConfigurations(context.Background(), r)
	is.True(err != nil)
	// make sure it's an invalid type error
	var iterr *yaml.InvalidTypeError
	is.True(cerrors.As(err, &iterr))
}

// replacingReader wraps a reader and replaces Old with New while reading.
type replacingReader struct {
	io.Reader
	Old []byte
	New []byte
}

func (rr replacingReader) Read(p []byte) (int, error) {
	i, err := rr.Reader.Read(p)
	if err != nil {
		return i, err
	}
	// that's very naive, Read reads up to len(p) bytes, so it could happen that
	// the sequence we are looking for is split in two
	// we don't care, it's good enough for our tests
	tmp := bytes.ReplaceAll(p, rr.Old, rr.New)
	copy(p, tmp)
	return i, nil
}
