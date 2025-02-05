// Copyright © 2025 Meroxa, Inc.
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

package pipelines

import (
	"fmt"
	"testing"

	"github.com/matryer/is"
)

func TestInitExecutionNoArgs(t *testing.T) {
	is := is.New(t)

	c := InitCommand{}
	err := c.Args([]string{})
	is.NoErr(err)
	is.Equal(defaultPipelineName, c.args.pipelineName)
}

func TestInitExecutionMultipleArgs(t *testing.T) {
	is := is.New(t)

	c := InitCommand{}
	err := c.Args([]string{"foo", "bar"})
	is.True((err != nil))
	is.Equal(err.Error(), "too many arguments")
}

func TestInitExecutionOneArg(t *testing.T) {
	is := is.New(t)

	pipelineName := "pipeline-name"

	c := InitCommand{}
	err := c.Args([]string{pipelineName})
	is.NoErr(err)
	is.Equal(pipelineName, c.args.pipelineName)
}

func TestInit_getPipelineName(t *testing.T) {
	tests := []struct {
		name             string
		argsPipelineName string
		flagsSource      string
		flagsDestination string
		expected         string
	}{
		{
			name:             "Custom pipeline name",
			argsPipelineName: "custom-pipeline",
			flagsSource:      "",
			flagsDestination: "",
			expected:         "custom-pipeline",
		},
		{
			name:             "Default pipeline name with custom source and destination",
			argsPipelineName: defaultPipelineName,
			flagsSource:      "custom-source",
			flagsDestination: "custom-destination",
			expected:         "custom-source-to-custom-destination",
		},
		{
			name:             "Default pipeline name with custom source only",
			argsPipelineName: defaultPipelineName,
			flagsSource:      "custom-source",
			flagsDestination: "",
			expected:         fmt.Sprintf("custom-source-to-%s", defaultDestination),
		},
		{
			name:             "Default pipeline name with custom destination only",
			argsPipelineName: defaultPipelineName,
			flagsSource:      "",
			flagsDestination: "custom-destination",
			expected:         fmt.Sprintf("%s-to-custom-destination", defaultSource),
		},
		{
			name:             "Default pipeline name with default source and destination",
			argsPipelineName: defaultPipelineName,
			flagsSource:      "",
			flagsDestination: "",
			expected:         fmt.Sprintf("%s-to-%s", defaultSource, defaultDestination),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			c := &InitCommand{}

			c.args.pipelineName = tt.argsPipelineName
			c.flags.Source = tt.flagsSource
			c.flags.Destination = tt.flagsDestination

			got := c.getPipelineName()
			is.Equal(got, tt.expected)
		})
	}
}
