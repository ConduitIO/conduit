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
	is.Equal("", c.args.pipelineName)
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
			argsPipelineName: demoPipelineName,
			flagsSource:      "custom-source",
			flagsDestination: "custom-destination",
			expected:         demoPipelineName,
		},
		{
			name:             "Default pipeline name with custom source only",
			argsPipelineName: demoPipelineName,
			flagsSource:      "custom-source",
			flagsDestination: "",
			expected:         demoPipelineName,
		},
		{
			name:             "Default pipeline name with custom destination only",
			argsPipelineName: demoPipelineName,
			flagsSource:      "",
			flagsDestination: "custom-destination",
			expected:         demoPipelineName,
		},
		{
			name:             "Default pipeline name with default source and destination",
			flagsSource:      "",
			flagsDestination: "",
			expected:         demoPipelineName,
		},
		{
			name:             "With custom source only",
			flagsSource:      "custom-source",
			flagsDestination: "",
			expected:         fmt.Sprintf("custom-source-to-%s", defaultDestination),
		},
		{
			name:             "With custom destination only",
			flagsSource:      "",
			flagsDestination: "custom-destination",
			expected:         fmt.Sprintf("%s-to-custom-destination", defaultSource),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			c := &InitCommand{}

			c.args.pipelineName = tt.argsPipelineName
			c.flags.Source = tt.flagsSource
			c.flags.Destination = tt.flagsDestination

			c.setSourceAndConnector()
			got := c.getPipelineName()
			is.Equal(got, tt.expected)
		})
	}
}

func TestInit_setSourceAndConnector(t *testing.T) {
	tests := []struct {
		name                string
		flagsSource         string
		flagsDestination    string
		expectedSource      string
		expectedDestination string
	}{
		{
			name:                "Default source and destination",
			flagsSource:         "",
			flagsDestination:    "",
			expectedSource:      defaultSource,
			expectedDestination: defaultDestination,
		},
		{
			name:                "Custom source only",
			flagsSource:         "custom-source",
			flagsDestination:    "",
			expectedSource:      "custom-source",
			expectedDestination: defaultDestination,
		},
		{
			name:                "Custom destination only",
			flagsSource:         "",
			flagsDestination:    "custom-destination",
			expectedSource:      defaultSource,
			expectedDestination: "custom-destination",
		},
		{
			name:                "Custom source and destination",
			flagsSource:         "custom-source",
			flagsDestination:    "custom-destination",
			expectedSource:      "custom-source",
			expectedDestination: "custom-destination",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			c := &InitCommand{}

			c.flags.Source = tt.flagsSource
			c.flags.Destination = tt.flagsDestination

			c.setSourceAndConnector()

			is.Equal(c.sourceConnector, tt.expectedSource)
			is.Equal(c.destinationConnector, tt.expectedDestination)
		})
	}
}

func TestInit_isDemoPipeline(t *testing.T) {
	tests := []struct {
		name             string
		flagsSource      string
		flagsDestination string
		expected         bool
	}{
		{
			name:             "Both source and destination empty",
			flagsSource:      "",
			flagsDestination: "",
			expected:         true,
		},
		{
			name:             "Source set, destination empty",
			flagsSource:      "custom-source",
			flagsDestination: "",
			expected:         false,
		},
		{
			name:             "Source empty, destination set",
			flagsSource:      "",
			flagsDestination: "custom-destination",
			expected:         false,
		},
		{
			name:             "Both source and destination set",
			flagsSource:      "custom-source",
			flagsDestination: "custom-destination",
			expected:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			c := &InitCommand{}

			c.flags.Source = tt.flagsSource
			c.flags.Destination = tt.flagsDestination

			got := c.isDemoPipeline()
			is.Equal(got, tt.expected)
		})
	}
}

func TestInit_getPipelineDescription(t *testing.T) {
	baseDesc := "This pipeline was initialized using the `conduit pipelines init` command.\n"

	demoDesc := func(src, dst string) string {
		return baseDesc + fmt.Sprintf("It is a demo pipeline that connects a source connector (%s) to a destination connector (%s).\n"+
			"Next step is simply run `conduit run` in your terminal and you should see a new record being logged every second.", src, dst)
	}

	customDesc := func(src, dst string) string {
		return baseDesc + fmt.Sprintf("It is a pipeline that connects a source connector (%s) to a destination connector (%s).\n"+
			"Make sure you update the configuration values before you run conduit via `conduit run", src, dst)
	}

	tests := []struct {
		name             string
		flagsSource      string
		flagsDestination string
		sourceConnector  string
		destConnector    string
		expected         string
	}{
		{
			name:             "Demo pipeline",
			flagsSource:      "",
			flagsDestination: "",
			sourceConnector:  defaultSource,
			destConnector:    defaultDestination,
			expected:         demoDesc(defaultSource, defaultDestination),
		},
		{
			name:             "Custom source and destination",
			flagsSource:      "custom-source",
			flagsDestination: "custom-destination",
			sourceConnector:  "custom-source",
			destConnector:    "custom-destination",
			expected:         customDesc("custom-source", "custom-destination"),
		},
		{
			name:             "Custom source only",
			flagsSource:      "custom-source",
			flagsDestination: "",
			sourceConnector:  "custom-source",
			destConnector:    defaultDestination,
			expected:         customDesc("custom-source", defaultDestination),
		},
		{
			name:             "Custom destination only",
			flagsSource:      "",
			flagsDestination: "custom-destination",
			sourceConnector:  defaultSource,
			destConnector:    "custom-destination",
			expected:         customDesc(defaultSource, "custom-destination"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			c := &InitCommand{}

			c.flags.Source = tt.flagsSource
			c.flags.Destination = tt.flagsDestination
			c.sourceConnector = tt.sourceConnector
			c.destinationConnector = tt.destConnector

			got := c.getPipelineDescription()
			is.Equal(got, tt.expected)
		})
	}
}
