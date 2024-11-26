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

package pipelines

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"text/template"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithDocs    = (*InitCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*InitCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*InitCommand)(nil)
	_ ecdysis.CommandWithExecute = (*InitCommand)(nil)

	//go:embed pipeline.tmpl
	pipelineCfgTmpl string
)

const (
	defaultSource      = "generator"
	defaultDestination = "file"
)

type InitArgs struct {
	name string
}

type InitFlags struct {
	Source        string `long:"source" usage:"Source connector (any of the built-in connectors)." default:"generator"`
	Destination   string `long:"destination" usage:"Destination connector (any of the built-in connectors)." default:"file"`
	PipelinesPath string `long:"pipelines.path" usage:"Path where the pipeline will be saved." default:"./pipelines"`
}

type InitCommand struct {
	args           InitArgs
	flags          InitFlags
	configFilePath string
}

func (c *InitCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	currentPath, err := os.Getwd()
	if err != nil {
		panic(cerrors.Errorf("failed to get current working directory: %w", err))
	}

	flags.SetDefault("pipelines.path", filepath.Join(currentPath, "./pipelines"))
	flags.SetDefault("source", defaultSource)
	flags.SetDefault("destination", defaultDestination)

	return flags
}

func (c *InitCommand) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a pipeline name")
	}

	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	c.args.name = args[0]
	return nil
}

func (c *InitCommand) Usage() string { return "init" }

func (c *InitCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Initialize an example pipeline.",
		Long: `Initialize a pipeline configuration file, with all of parameters for source and destination connectors 
initialized and described. The source and destination connector can be chosen via flags. If no connectors are chosen, then
a simple and runnable generator-to-log pipeline is configured.`,
		Example: "conduit pipelines init awesome-pipeline-name --source postgres --destination kafka --pipelines.path pipelines/pg-to-kafka.yaml",
	}
}

func (c *InitCommand) getSourceParams() (connectorTemplate, error) {
	for _, conn := range builtin.DefaultBuiltinConnectors {
		specs := conn.NewSpecification()
		if specs.Name == c.flags.Source || specs.Name == "builtin:"+c.flags.Source {
			if conn.NewSource == nil {
				return connectorTemplate{}, cerrors.Errorf("plugin %v has no source", c.flags.Source)
			}

			return connectorTemplate{
				Name:   specs.Name,
				Params: conn.NewSource().Parameters(),
			}, nil
		}
	}

	return connectorTemplate{}, cerrors.Errorf("%v: %w", c.flags.Source, plugin.ErrPluginNotFound)
}

func (c *InitCommand) getDestinationParams() (connectorTemplate, error) {
	for _, conn := range builtin.DefaultBuiltinConnectors {
		specs := conn.NewSpecification()
		if specs.Name == c.flags.Destination || specs.Name == "builtin:"+c.flags.Destination {
			if conn.NewDestination == nil {
				return connectorTemplate{}, cerrors.Errorf("plugin %v has no source", c.flags.Destination)
			}

			return connectorTemplate{
				Name:   specs.Name,
				Params: conn.NewDestination().Parameters(),
			}, nil
		}
	}

	return connectorTemplate{}, cerrors.Errorf("%v: %w", c.flags.Destination, plugin.ErrPluginNotFound)
}

func (c *InitCommand) buildDemoPipeline() pipelineTemplate {
	srcParams, _ := c.getSourceParams()
	dstParams, _ := c.getDestinationParams()

	return pipelineTemplate{
		Name: c.args.name,
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

func (c *InitCommand) buildTemplatePipeline() (pipelineTemplate, error) {
	srcParams, err := c.getSourceParams()
	if err != nil {
		return pipelineTemplate{}, cerrors.Errorf("failed getting source params: %w", err)
	}

	dstParams, err := c.getDestinationParams()
	if err != nil {
		return pipelineTemplate{}, cerrors.Errorf("failed getting destination params: %w", err)
	}

	return pipelineTemplate{
		Name:            c.args.name,
		SourceSpec:      srcParams,
		DestinationSpec: dstParams,
	}, nil
}

func (c *InitCommand) getOutput() *os.File {
	output, err := os.OpenFile(c.configFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		log.Fatalf("error: failed to open %s: %v", c.configFilePath, err)
	}

	return output
}

func (c *InitCommand) write(pipeline pipelineTemplate) error {
	t, err := template.New("").Funcs(funcMap).Option("missingkey=zero").Parse(pipelineCfgTmpl)
	if err != nil {
		return cerrors.Errorf("failed parsing template: %w", err)
	}

	output := c.getOutput()
	defer output.Close()

	err = t.Execute(output, pipeline)
	if err != nil {
		return cerrors.Errorf("failed executing template: %w", err)
	}

	return nil
}

func (c *InitCommand) Execute(_ context.Context) error {
	c.configFilePath = filepath.Join(c.flags.PipelinesPath, fmt.Sprintf("pipeline-%s.yaml", c.args.name))

	// TODO: utilize buildDemoPipeline if source and destination are the default ones

	pipeline, err := c.buildTemplatePipeline()
	if err != nil {
		return err
	}

	if err := c.write(pipeline); err != nil {
		return cerrors.Errorf("could not write pipeline: %w", err)
	}

	fmt.Printf(`Your pipeline has been initialized and created at %s.

To run the pipeline, simply run 'conduit'.`, c.configFilePath)

	return nil
}
