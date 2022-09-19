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
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

func TestParser_Success(t *testing.T) {
	is := is.New(t)
	filename, err := filepath.Abs("./test/pipelines1-success.yml")
	if err != nil {
		t.Error(err)
	}
	want := map[string]PipelineConfig{
		"pipeline1": {
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Processors: map[string]ProcessorConfig{
				"pipeline1proc1": {
					Type: "js",
					Settings: map[string]string{
						"additionalProp1": "string",
						"additionalProp2": "string",
					},
				},
			},
			Connectors: map[string]ConnectorConfig{
				"con1": {
					Type:   "source",
					Plugin: "builtin:s3",
					Name:   "s3-source",
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
			Description: "desc2",
			Connectors: map[string]ConnectorConfig{
				"con2": {
					Type:   "destination",
					Plugin: "builtin:file",
					Name:   "file-dest",
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

	data, err := os.ReadFile(filename)
	if err != nil {
		t.Error(err)
	}

	got, err := Parse(data)
	is.NoErr(err)
	is.Equal(want, got)
}

func TestParser_DuplicatePipelineId(t *testing.T) {
	is := is.New(t)
	filename, err := filepath.Abs("./test/pipelines2-duplicate-pipeline-id.yml")
	if err != nil {
		t.Error(err)
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		t.Error(err)
	}

	p, err := Parse(data)
	is.True(err != nil)
	is.Equal(p, nil)
}

func TestParser_UnsupportedVersion(t *testing.T) {
	is := is.New(t)
	filename, err := filepath.Abs("./test/pipelines3-unsupported-version.yml")
	if err != nil {
		t.Error(err)
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		t.Error(err)
	}

	p, err := Parse(data)
	is.True(err != nil)
	is.Equal(cerrors.Is(err, ErrUnsupportedVersion), true)
	is.Equal(p, nil)
}

func TestParser_MissingVersion(t *testing.T) {
	is := is.New(t)
	filename, err := filepath.Abs("./test/pipelines4-missing-version.yml")
	if err != nil {
		t.Error(err)
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		t.Error(err)
	}

	p, err := Parse(data)
	is.True(err != nil)
	is.Equal(cerrors.Is(err, ErrUnsupportedVersion), true)
	is.Equal(p, nil)
}

func TestParser_EmptyFile(t *testing.T) {
	is := is.New(t)
	filename, err := filepath.Abs("./test/pipelines5-empty.yml")
	if err != nil {
		t.Error(err)
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		t.Error(err)
	}

	p, err := Parse(data)
	is.NoErr(err)
	is.Equal(p, nil)
}

func TestParser_InvalidYaml(t *testing.T) {
	is := is.New(t)
	filename, err := filepath.Abs("./test/pipelines6-invalid-yaml.yml")
	if err != nil {
		t.Error(err)
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		t.Error(err)
	}

	p, err := Parse(data)
	is.True(err != nil)
	is.Equal(p, nil)
}

func TestParser_EnvVars(t *testing.T) {
	is := is.New(t)
	filename, err := filepath.Abs("./test/pipelines7-env-vars.yml")
	if err != nil {
		t.Error(err)
	}

	// set env variables
	err = os.Setenv("TEST_PARSER_AWS_SECRET", "my-aws-secret")
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

	want := map[string]PipelineConfig{
		"pipeline1": {
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: map[string]ConnectorConfig{
				"con1": {
					Type:   "source",
					Plugin: "builtin:s3",
					Name:   "s3-source",
					Settings: map[string]string{
						// env variables should be replaces with their values
						"aws.secret": "my-aws-secret",
						"aws.key":    "my-aws-key",
						"aws.url":    "my/aws-url/url",
					},
				},
			},
		},
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		t.Error(err)
	}

	got, err := Parse(data)
	is.NoErr(err)
	is.Equal(want, got)
}
