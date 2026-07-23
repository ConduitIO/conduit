// Copyright © 2026 Meroxa, Inc.
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

package conduit_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	conduit "github.com/conduitio/conduit"
	"github.com/conduitio/conduit/pkg/foundation/log"
	provisioningconfig "github.com/conduitio/conduit/pkg/provisioning/config"
	yamlparser "github.com/conduitio/conduit/pkg/provisioning/config/yaml"
	v2 "github.com/conduitio/conduit/pkg/provisioning/config/yaml/v2"
	yaml "github.com/conduitio/yaml/v3"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
)

// yamlRoundTrip serializes cfg through the identical YAML path a config
// file takes — v2.FromConfig, then yaml.Marshal — and parses the result
// back through the production yaml.Parser (the same parser the file-based
// provisioning path/`conduit run` use). It shares no code with builder.go,
// which is what makes it an independent check of AC-6's round-trip
// property, not a tautological one.
func yamlRoundTrip(t *testing.T, cfg provisioningconfig.Pipeline) provisioningconfig.Pipeline {
	t.Helper()
	is := is.New(t)

	doc := v2.FromConfig([]provisioningconfig.Pipeline{cfg})
	data, err := yaml.Marshal(doc)
	is.NoErr(err)

	parser := yamlparser.NewParser(log.Nop())
	got, err := parser.Parse(context.Background(), bytes.NewReader(data))
	is.NoErr(err)
	is.Equal(len(got), 1)

	return got[0]
}

// TestPipelineBuilder_RoundTrip is AC-6's property test: for a large,
// deterministic corpus spanning every v2 schema field — pipeline
// status/name/description, connector count/type/settings/nested
// processors, pipeline-scoped processors (workers/condition/settings), and
// every DLQ variant including the nil-vs-explicit-zero *int distinction
// (§7 of the embed workstream plan) — build(x) must equal
// parse(marshal(fromConfig(build(x)))). A deterministic combinatorial
// matrix is used instead of a randomized generator (no pgregory.net/rapid
// or gopter dependency exists in this module yet, and CLAUDE.md requires
// justifying new dependencies) — every sub-test name is reproducible and
// failures are directly reportable, which a fixed matrix gives for free.
func TestPipelineBuilder_RoundTrip(t *testing.T) {
	statuses := []string{"", conduit.StatusRunning, conduit.StatusStopped}
	names := []string{"", "custom-name"}
	descriptions := []string{"", "a description"}

	type dlqCase struct {
		name string
		dlq  func() *conduit.DLQBuilder // nil means "never call WithDLQ"
	}
	dlqVariants := []dlqCase{
		{name: "none", dlq: nil},
		{name: "plugin-only-nil-windows", dlq: func() *conduit.DLQBuilder {
			return conduit.NewDLQ("builtin:log")
		}},
		{name: "explicit-zero-windows", dlq: func() *conduit.DLQBuilder {
			return conduit.NewDLQ("builtin:log").WithWindowSize(0).WithWindowNackThreshold(0)
		}},
		{name: "full", dlq: func() *conduit.DLQBuilder {
			return conduit.NewDLQ("builtin:log").WithSetting("k", "v").WithWindowSize(4).WithWindowNackThreshold(2)
		}},
	}

	connectorCounts := []int{0, 1, 2}
	processorCounts := []int{0, 1, 2}

	caseNum := 0
	for _, status := range statuses {
		for _, name := range names {
			for _, desc := range descriptions {
				for _, dlqVariant := range dlqVariants {
					for _, numConnectors := range connectorCounts {
						for _, numProcessors := range processorCounts {
							caseNum++
							testName := fmt.Sprintf(
								"status=%q/name=%q/desc=%q/dlq=%s/connectors=%d/processors=%d",
								status, name, desc, dlqVariant.name, numConnectors, numProcessors,
							)
							t.Run(testName, func(t *testing.T) {
								is := is.New(t)

								b := conduit.NewPipeline(fmt.Sprintf("gen-%d", caseNum)).
									WithStatus(status).
									WithName(name).
									WithDescription(desc)

								for c := 0; c < numConnectors; c++ {
									var cb *conduit.ConnectorBuilder
									if c%2 == 0 {
										cb = conduit.NewSourceConnector(fmt.Sprintf("conn-%d", c), "builtin:generator")
									} else {
										cb = conduit.NewDestinationConnector(fmt.Sprintf("conn-%d", c), "builtin:log")
									}
									cb = cb.WithSetting("k", fmt.Sprintf("v%d", c))
									for p := 0; p < numProcessors; p++ {
										cb = cb.WithProcessor(
											conduit.NewProcessor(fmt.Sprintf("conn-%d-proc-%d", c, p), "js").
												WithWorkers(p).
												WithCondition("true"),
										)
									}
									b = b.WithConnector(cb)
								}

								for p := 0; p < numProcessors; p++ {
									b = b.WithProcessor(
										conduit.NewProcessor(fmt.Sprintf("pipeline-proc-%d", p), "js").
											WithSetting("x", "y"),
									)
								}

								if dlqVariant.dlq != nil {
									b = b.WithDLQ(dlqVariant.dlq())
								}

								built, err := b.Build()
								is.NoErr(err)

								roundTripped := yamlRoundTrip(t, built)
								if diff := cmp.Diff(built, roundTripped); diff != "" {
									t.Fatalf("round-trip mismatch (-built +roundTripped):\n%s", diff)
								}
							})
						}
					}
				}
			}
		}
	}
	t.Logf("ran %d round-trip cases", caseNum)
}

// TestPipelineBuilder_RoundTrip_Fixtures reproduces real, already-committed
// YAML fixtures (pkg/provisioning/config/yaml/v2/testdata/
// pipelines1-success.yml) field-for-field via the builder — AC-6's other
// required direction: not just build->YAML->parse, but YAML->parse->assert
// the builder can reproduce it. Using the existing production testdata
// (rather than new throwaway fixtures) means these assertions are checked
// against pipeline shapes the yaml parser's own test suite already commits
// to keeping stable.
func TestPipelineBuilder_RoundTrip_Fixtures(t *testing.T) {
	const fixturePath = "pkg/provisioning/config/yaml/v2/testdata/pipelines1-success.yml"

	parsed := parseFixture(t, fixturePath)
	byID := make(map[string]provisioningconfig.Pipeline, len(parsed))
	for _, p := range parsed {
		byID[p.ID] = p
	}

	windowSize, windowNackThreshold := 4, 2
	tests := []struct {
		name string
		want string // pipeline ID in the fixture
		got  func() (conduit.PipelineConfig, error)
	}{
		{
			name: "pipeline1: connector with nested processor, pipeline processor, full DLQ",
			want: "pipeline1",
			got: func() (conduit.PipelineConfig, error) {
				return conduit.NewPipeline("pipeline1").
					WithStatus(conduit.StatusRunning).
					WithName("pipeline1").
					WithDescription("desc1").
					WithConnector(
						conduit.NewSourceConnector("con1", "builtin:s3").
							WithName("s3-source").
							WithSettings(map[string]string{
								"aws.region": "us-east-1",
								"aws.bucket": "my-bucket",
							}).
							WithProcessor(
								conduit.NewProcessor("proc1", "js").
									WithSettings(map[string]string{
										"additionalProp1": "string",
										"additionalProp2": "string",
									}),
							),
					).
					WithProcessor(
						conduit.NewProcessor("pipeline1proc1", "js").
							WithSettings(map[string]string{
								"additionalProp1": "string",
								"additionalProp2": "string",
							}),
					).
					WithDLQ(
						conduit.NewDLQ("my-plugin").
							WithSetting("foo", "bar").
							WithWindowSize(windowSize).
							WithWindowNackThreshold(windowNackThreshold),
					).
					Build()
			},
		},
		{
			name: "pipeline2: destination connector with nested processor, no DLQ, stopped",
			want: "pipeline2",
			got: func() (conduit.PipelineConfig, error) {
				return conduit.NewPipeline("pipeline2").
					WithStatus(conduit.StatusStopped).
					WithName("pipeline2").
					WithDescription("desc2").
					WithConnector(
						conduit.NewDestinationConnector("con2", "builtin:file").
							WithName("file-dest").
							WithSetting("path", "my/path").
							WithProcessor(
								conduit.NewProcessor("con2proc1", "hoistfield").
									WithSettings(map[string]string{
										"additionalProp1": "string",
										"additionalProp2": "string",
									}),
							),
					).
					Build()
			},
		},
		{
			name: "pipeline3: minimal pipeline, no connectors/processors/DLQ",
			want: "pipeline3",
			got: func() (conduit.PipelineConfig, error) {
				return conduit.NewPipeline("pipeline3").
					WithStatus(conduit.StatusStopped).
					WithName("pipeline3").
					WithDescription("empty").
					Build()
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			want, ok := byID[tc.want]
			is.True(ok) // fixture must contain the pipeline ID this test targets

			got, err := tc.got()
			is.NoErr(err)

			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("builder did not reproduce fixture %q (-fixture +builder):\n%s", tc.want, diff)
			}
		})
	}
}

// parseFixture parses a pipeline-config YAML file from the module root
// using the production yaml.Parser, the same parser `conduit run`'s
// file-based provisioning path uses.
func parseFixture(t *testing.T, relPath string) []provisioningconfig.Pipeline {
	t.Helper()
	is := is.New(t)

	wd, err := os.Getwd()
	is.NoErr(err)

	f, err := os.Open(filepath.Join(wd, relPath))
	is.NoErr(err)
	defer f.Close()

	parser := yamlparser.NewParser(log.Nop())
	pipelines, err := parser.Parse(context.Background(), f)
	is.NoErr(err)

	return pipelines
}
