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
	"strings"
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
	intPtr := func(i int) *int { return &i }
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
						WindowSize:          intPtr(4),
						WindowNackThreshold: intPtr(2),
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

// TestParser_WarningsExposure_DoesNotChangeRunBehavior is the AC-4 guard for
// docs/design-documents/20260708-cli-pipeline-inspect-lint-dryrun.md: adding
// ParseWithWarnings (the new, additive channel `lint`/`dry-run` use to
// receive warnings instead of only logging them) must not change what
// `conduit run` observes on the ParseConfigurations/Parse path — Service
// still calls Parse, still only gets ([]config.Pipeline, error) back, and
// still sees every warning logged, byte-for-byte identical to before
// Warning/Warnings existed (see TestParser_V1_Warnings above, which this
// test's first half re-asserts via the run path specifically).
func TestParser_WarningsExposure_DoesNotChangeRunBehavior(t *testing.T) {
	is := is.New(t)
	filepath := "./v1/testdata/pipelines1-success.yml"

	// --- the `run` path: pkg/provisioning.Service.parsePipelineConfigFile
	// calls exactly Parser.Parse -> ParseConfigurations. It must keep
	// logging warnings and must keep NOT returning them.
	var runLog bytes.Buffer
	runParser := NewParser(log.New(zerolog.New(&runLog)))

	runFile, err := os.Open(filepath)
	is.NoErr(err)
	defer runFile.Close()

	runPipelines, err := runParser.Parse(context.Background(), runFile)
	is.NoErr(err)
	is.True(len(runPipelines) > 0) // run still gets its pipelines back

	wantRunLog := `{"level":"warn","component":"yaml.Parser","line":5,"column":5,"field":"unknownField","message":"field unknownField not found in type v1.Pipeline"}
{"level":"warn","component":"yaml.Parser","line":17,"column":9,"field":"processors","message":"the order of processors is non-deterministic in configuration files with version 1.x, please upgrade to version 2.x"}
{"level":"warn","component":"yaml.Parser","line":23,"column":5,"field":"processors","message":"the order of processors is non-deterministic in configuration files with version 1.x, please upgrade to version 2.x"}
{"level":"warn","component":"yaml.Parser","line":30,"column":5,"field":"dead-letter-queue","message":"field dead-letter-queue was introduced in version 1.1, please update the pipeline config version"}
{"level":"warn","component":"yaml.Parser","line":38,"column":1,"field":"version","value":"1.12","message":"unrecognized version 1.12, falling back to parser version 1.1"}
{"level":"warn","component":"yaml.Parser","line":51,"column":9,"field":"processors","message":"the order of processors is non-deterministic in configuration files with version 1.x, please upgrade to version 2.x"}
`
	is.Equal(runLog.String(), wantRunLog) // run's logged warnings are byte-for-byte unchanged by the refactor

	// --- the new, additive `lint`/`dry-run` channel: a separate Parser
	// instance (its own logger/capture) over the same file, via
	// ParseWithWarnings instead of Parse.
	var lintLog bytes.Buffer
	lintParser := NewParser(log.New(zerolog.New(&lintLog)))

	lintFile, err := os.Open(filepath)
	is.NoErr(err)
	defer lintFile.Close()

	lintPipelines, warnings, err := lintParser.ParseWithWarnings(context.Background(), lintFile)
	is.NoErr(err)
	is.Equal(len(lintPipelines), len(runPipelines)) // same parse result as the run path
	is.Equal(len(warnings), 6)                      // same 6 warnings the run path logged, returned instead
	is.Equal(lintLog.String(), "")                  // ParseWithWarnings never logs on its own; a caller renders/returns them

	// Spot-check the located fields lint's Finding needs (line/column/field).
	is.Equal(warnings[0].Line, 5)
	is.Equal(warnings[0].Column, 5)
	is.Equal(warnings[0].Field, "unknownField")
	is.Equal(warnings[0].Message, "field unknownField not found in type v1.Pipeline")
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

	// set env variables; t.Setenv restores the previous value (and clears vars
	// that were unset) when the test finishes, so they never leak into other
	// tests in this package.
	t.Setenv("TEST_PARSER_AWS_SECRET", "my-aws-secret")
	t.Setenv("TEST_PARSER_AWS_KEY", "my-aws-key")
	t.Setenv("TEST_PARSER_AWS_URL", "aws-url")

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
	intPtr := func(i int) *int { return &i }
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
						WindowSize:          intPtr(4),
						WindowNackThreshold: intPtr(2),
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

// TestParser_V2_MultiDocument is the regression suite for #2255: a bad document
// in a multi-document file must be isolated to that document, leaving the valid
// documents around it parsed in order. It exercises every position and failure
// mode (unrecognized version, non-recoverable decode error, and syntax error) to
// pin the two-decoder synchronization — a desync here would silently drop or
// misattribute valid pipelines, which would be worse than the original bug.
func TestParser_V2_MultiDocument(t *testing.T) {
	// valid single-pipeline document for the given id
	pipe := func(id string) string {
		return "version: 2.2\npipelines:\n  - id: " + id + "\n"
	}
	// valid version, but an unrecognized major version -> parseVersion error path
	badVersion := func(v string) string {
		return "version: " + v + "\npipelines:\n  - id: bad\n"
	}
	// recognized version, but pipelines is the wrong type -> config decode error path
	decodeErr := "version: 2.2\npipelines: \"not a list\"\n"
	// unterminated flow sequence -> YAML syntax error (stops parsing this file)
	syntaxErr := "version: 2.2\npipelines: [unterminated\n"

	join := func(docs ...string) string { return strings.Join(docs, "---\n") }

	testCases := []struct {
		name    string
		input   string
		wantErr bool
		wantIDs []string
	}{
		{"all valid", join(pipe("a"), pipe("b")), false, []string{"a", "b"}},
		{"invalid version middle", join(pipe("a"), badVersion("4"), pipe("c")), true, []string{"a", "c"}},
		{"invalid version first", join(badVersion("4"), pipe("a"), pipe("b")), true, []string{"a", "b"}},
		{"invalid version last", join(pipe("a"), pipe("b"), badVersion("9")), true, []string{"a", "b"}},
		{"two consecutive invalid versions", join(pipe("a"), badVersion("8"), badVersion("9"), pipe("b")), true, []string{"a", "b"}},
		{"decode error middle", join(pipe("a"), decodeErr, pipe("c")), true, []string{"a", "c"}},
		{"decode error first", join(decodeErr, pipe("a"), pipe("b")), true, []string{"a", "b"}},
		{"two consecutive decode errors", join(pipe("a"), decodeErr, decodeErr, pipe("b")), true, []string{"a", "b"}},
		// a genuine syntax error can't be resynced, so parsing stops but keeps the
		// documents parsed before it (strictly better than dropping the whole file)
		{"syntax error keeps prior", join(pipe("a"), syntaxErr, pipe("c")), true, []string{"a"}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			parser := NewParser(log.Nop())
			got, err := parser.ParseConfigurations(context.Background(), bytes.NewBufferString(tc.input))
			if tc.wantErr {
				is.True(err != nil)
			} else {
				is.NoErr(err)
			}
			gotIDs := make([]string, len(got))
			for i, c := range got {
				gotIDs[i] = c.(v2.Configuration).Pipelines[0].ID
			}
			is.Equal(gotIDs, tc.wantIDs) // valid documents parsed, in order; bad ones skipped
		})
	}
}

// TestParser_V2_MultiDocument_IsolatesInvalidVersion pins the specific #2255
// error message so the offending document remains identifiable.
func TestParser_V2_MultiDocument_IsolatesInvalidVersion(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	doc := "version: 2.2\npipelines:\n  - id: a\n---\nversion: 4\npipelines:\n  - id: bad\n"
	_, err := parser.ParseConfigurations(context.Background(), bytes.NewBufferString(doc))
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "unrecognized version 4"))
}

func TestParser_V2_EnvVars(t *testing.T) {
	is := is.New(t)
	parser := NewParser(log.Nop())
	filepath := "./v2/testdata/pipelines5-env-vars.yml"

	// set env variables; t.Setenv restores the previous value (and clears vars
	// that were unset) when the test finishes, so they never leak into other
	// tests in this package.
	t.Setenv("TEST_PARSER_AWS_SECRET", "my-aws-secret")
	t.Setenv("TEST_PARSER_AWS_KEY", "my-aws-key")
	t.Setenv("TEST_PARSER_AWS_URL", "aws-url")

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
