// Copyright © 2024 Meroxa, Inc.
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

package cli

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
)

//go:embed pipeline.tmpl
var pipelineCfgTmpl string

var funcMap = template.FuncMap{
	"formatParameterValueTable":      formatParameterValueTable,
	"formatParameterValueYAML":       formatParameterValueYAML,
	"formatParameterDescriptionYAML": formatParameterDescriptionYAML,
	"formatParameterRequired":        formatParameterRequired,
}

func formatParameterRequired(param config.Parameter) string {
	for _, v := range param.Validations {
		if v.Type() == config.ValidationTypeRequired {
			return "Required"
		}
	}

	return "Optional"
}

// formatParameterValue formats the value of a configuration parameter.
func formatParameterValueTable(value string) string {
	switch {
	case value == "":
		return `<Chip label="null" />`
	case strings.Contains(value, "\n"):
		// specifically used in the javascript processor
		return fmt.Sprintf("\n```js\n%s\n```\n", value)
	default:
		return fmt.Sprintf("`%s`", value)
	}
}

func formatParameterDescriptionYAML(description string) string {
	const (
		indentLen  = 10
		prefix     = "# "
		lineLen    = 80
		tmpNewLine = "〠"
	)

	// remove markdown new lines
	description = strings.ReplaceAll(description, "\n\n", tmpNewLine)
	description = strings.ReplaceAll(description, "\n", " ")
	description = strings.ReplaceAll(description, tmpNewLine, "\n")

	formattedDescription := formatMultiline(description, strings.Repeat(" ", indentLen)+prefix, lineLen)
	// remove first indent and last new line
	formattedDescription = formattedDescription[indentLen : len(formattedDescription)-1]
	return formattedDescription
}

func formatMultiline(
	input string,
	prefix string,
	maxLineLen int,
) string {
	textLen := maxLineLen - len(prefix)

	// split the input into lines of length textLen
	lines := strings.Split(input, "\n")
	var formattedLines []string
	for _, line := range lines {
		if len(line) <= textLen {
			formattedLines = append(formattedLines, line)
			continue
		}

		// split the line into multiple lines, don't break words
		words := strings.Fields(line)
		var formattedLine string
		for _, word := range words {
			if len(formattedLine)+len(word) > textLen {
				formattedLines = append(formattedLines, formattedLine[1:])
				formattedLine = ""
			}
			formattedLine += " " + word
		}
		if formattedLine != "" {
			formattedLines = append(formattedLines, formattedLine[1:])
		}
	}

	// combine lines including indent and prefix
	var formatted string
	for _, line := range formattedLines {
		formatted += prefix + line + "\n"
	}

	return formatted
}

func formatParameterValueYAML(value string) string {
	switch {
	case value == "":
		return `""`
	case strings.Contains(value, "\n"):
		// specifically used in the javascript processor
		formattedValue := formatMultiline(value, "            ", 10000)
		return fmt.Sprintf("|\n%s", formattedValue)
	default:
		return fmt.Sprintf(`'%s'`, value)
	}
}

const (
	defaultDestination = "file"
	defaultSource      = "generator"
)

type pipelineTemplate struct {
	Name            string
	SourceSpec      connectorTemplate
	DestinationSpec connectorTemplate
}

type connectorTemplate struct {
	Name   string
	Params config.Parameters
}

type PipelinesInitArgs struct {
	Name        string
	Source      string
	Destination string
	Path        string
}

type PipelinesInit struct {
	args PipelinesInitArgs
}

func NewPipelinesInit(args PipelinesInitArgs) *PipelinesInit {
	return &PipelinesInit{args: args}
}

func (pi *PipelinesInit) Run() error {
	var pipeline pipelineTemplate
	// if no source/destination arguments are provided,
	// we build a runnable example pipeline
	if pi.args.Source == "" && pi.args.Destination == "" {
		pipeline = pi.buildDemoPipeline()
	} else {
		p, err := pi.buildTemplatePipeline()
		if err != nil {
			return err
		}
		pipeline = p
	}

	err := pi.write(pipeline)
	if err != nil {
		return cerrors.Errorf("could not write pipeline: %w", err)
	}

	fmt.Printf(`Your pipeline has been initialized and created at %s.

To run the pipeline, execute:

conduit --pipelines.path %s`,
		pi.configFilePath(), pi.configFilePath())

	return nil
}

func (pi *PipelinesInit) buildTemplatePipeline() (pipelineTemplate, error) {
	srcParams, err := pi.getSourceParams()
	if err != nil {
		return pipelineTemplate{}, cerrors.Errorf("failed getting source params: %w", err)
	}

	dstParams, err := pi.getDestinationParams()
	if err != nil {
		return pipelineTemplate{}, cerrors.Errorf("failed getting destination params: %w", err)
	}

	return pipelineTemplate{
		Name:            pi.pipelineName(),
		SourceSpec:      srcParams,
		DestinationSpec: dstParams,
	}, nil
}

func (pi *PipelinesInit) buildDemoPipeline() pipelineTemplate {
	srcParams, _ := pi.getSourceParams()
	dstParams, _ := pi.getDestinationParams()

	return pipelineTemplate{
		Name: pi.pipelineName(),
		SourceSpec: connectorTemplate{
			Name: defaultSource,
			Params: map[string]config.Parameter{
				"format.type": {
					Description: srcParams.Params["format.type"].Description,
					Type:        srcParams.Params["format.type"].Type,
					Default:     "structured",
					Validations: srcParams.Params["format.type"].Validations,
				},
				"format.options.scheduledDeparture": {
					Description: "Generate field 'scheduledDeparture' of type 'time'",
					Type:        config.ParameterTypeString,
					Default:     "time",
				},
				"format.options.airline": {
					Description: "Generate field 'airline' of type string",
					Type:        config.ParameterTypeString,
					Default:     "string",
				},
				"rate": {
					Description: srcParams.Params["rate"].Description,
					Type:        srcParams.Params["rate"].Type,
					Default:     "1",
				},
			},
		},
		DestinationSpec: connectorTemplate{
			Name: defaultDestination,
			Params: map[string]config.Parameter{
				"path": {
					Description: dstParams.Params["path"].Description,
					Type:        dstParams.Params["path"].Type,
					Default:     "./destination.txt",
				},
			},
		},
	}
}

func (pi *PipelinesInit) getOutput() *os.File {
	output, err := os.OpenFile(pi.configFilePath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		log.Fatalf("error: failed to open %s: %v", pi.args.Path, err)
	}

	return output
}

func (pi *PipelinesInit) write(pipeline pipelineTemplate) error {
	t, err := template.New("").Funcs(funcMap).Option("missingkey=zero").Parse(pipelineCfgTmpl)
	if err != nil {
		return cerrors.Errorf("failed parsing template: %w", err)
	}

	output := pi.getOutput()
	defer output.Close()

	err = t.Execute(output, pipeline)
	if err != nil {
		return cerrors.Errorf("failed executing template: %w", err)
	}

	return nil
}

func (pi *PipelinesInit) getSourceParams() (connectorTemplate, error) {
	for _, conn := range builtin.DefaultBuiltinConnectors {
		specs := conn.NewSpecification()
		if specs.Name == pi.sourceConnector() || specs.Name == "builtin:"+pi.sourceConnector() {
			if conn.NewSource == nil {
				return connectorTemplate{}, cerrors.Errorf("plugin %v has no source", pi.sourceConnector())
			}

			return connectorTemplate{
				Name:   specs.Name,
				Params: conn.NewSource().Parameters(),
			}, nil
		}
	}

	return connectorTemplate{}, cerrors.Errorf("%v: %w", pi.sourceConnector(), plugin.ErrPluginNotFound)
}

func (pi *PipelinesInit) getDestinationParams() (connectorTemplate, error) {
	for _, conn := range builtin.DefaultBuiltinConnectors {
		specs := conn.NewSpecification()
		if specs.Name == pi.destinationConnector() || specs.Name == "builtin:"+pi.destinationConnector() {
			if conn.NewDestination == nil {
				return connectorTemplate{}, cerrors.Errorf("plugin %v has no source", pi.destinationConnector())
			}

			return connectorTemplate{
				Name:   specs.Name,
				Params: conn.NewDestination().Parameters(),
			}, nil
		}
	}

	return connectorTemplate{}, cerrors.Errorf("%v: %w", pi.destinationConnector(), plugin.ErrPluginNotFound)
}

func (pi *PipelinesInit) configFilePath() string {
	path := pi.args.Path
	if path == "" {
		path = "./pipelines"
	}

	return filepath.Join(path, pi.configFileName())
}

func (pi *PipelinesInit) configFileName() string {
	return fmt.Sprintf("pipeline-%s.yaml", pi.pipelineName())
}

func (pi *PipelinesInit) sourceConnector() string {
	if pi.args.Source != "" {
		return pi.args.Source
	}

	return defaultSource
}

func (pi *PipelinesInit) destinationConnector() string {
	if pi.args.Destination != "" {
		return pi.args.Destination
	}

	return defaultDestination
}

func (pi *PipelinesInit) pipelineName() string {
	if pi.args.Name != "" {
		return pi.args.Name
	}

	return fmt.Sprintf("%s-to-%s", pi.sourceConnector(), pi.destinationConnector())
}
