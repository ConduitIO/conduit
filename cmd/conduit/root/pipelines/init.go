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
	defaultSource       = "generator"
	defaultDestination  = "file"
	defaultPipelineName = "generator-to-file"
)

type InitArgs struct {
	pipelineName string
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
	flags.SetDefault("source", "generator")
	flags.SetDefault("destination", "file")
	return flags
}

func (c *InitCommand) Args(args []string) error {
	switch len(args) {
	case 0:
		c.args.pipelineName = defaultPipelineName
	case 1:
		c.args.pipelineName = args[0]
	default:
		return cerrors.Errorf("too many arguments")
	}
	return nil
}

func (c *InitCommand) Usage() string { return "init [PIPELINE_NAME]" }

func (c *InitCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Initialize an example pipeline.",
		Long: `Initialize a pipeline configuration file, with all of parameters for source and destination connectors 
initialized and described. The source and destination connector can be chosen via flags. If no connectors are chosen, then
a simple and runnable generator-to-log pipeline is configured. `,
		Example: "conduit pipelines init\n" +
			"conduit pipelines init awesome-pipeline-name --source postgres --destination kafka \n" +
			"conduit pipelines init file-to-pg --source file --destination postgres --pipelines.path ./my-pipelines",
	}
}

func (c *InitCommand) getSourceSpec() (connectorSpec, error) {
	for _, conn := range builtin.DefaultBuiltinConnectors {
		specs := conn.NewSpecification()
		if specs.Name == c.flags.Source || specs.Name == "builtin:"+c.flags.Source {
			if conn.NewSource == nil {
				return connectorSpec{}, cerrors.Errorf("plugin %v has no source", c.flags.Source)
			}

			return connectorSpec{
				Name:   specs.Name,
				Params: conn.NewSpecification().SourceParams,
			}, nil
		}
	}

	return connectorSpec{}, cerrors.Errorf("%v: %w", c.flags.Source, plugin.ErrPluginNotFound)
}

func (c *InitCommand) getDestinationSpec() (connectorSpec, error) {
	for _, conn := range builtin.DefaultBuiltinConnectors {
		specs := conn.NewSpecification()
		if specs.Name == c.flags.Destination || specs.Name == "builtin:"+c.flags.Destination {
			if conn.NewDestination == nil {
				return connectorSpec{}, cerrors.Errorf("plugin %v has no source", c.flags.Destination)
			}

			return connectorSpec{
				Name:   specs.Name,
				Params: conn.NewSpecification().DestinationParams,
			}, nil
		}
	}
	return connectorSpec{}, cerrors.Errorf("%v: %w", c.flags.Destination, plugin.ErrPluginNotFound)
}

// getDemoSourceGeneratorSpec returns a simplified version of the source generator connector.
func (c *InitCommand) getDemoSourceGeneratorSpec(spec connectorSpec) connectorSpec {
	return connectorSpec{
		Name: defaultSource,
		Params: map[string]config.Parameter{
			"format.type": {
				Description: spec.Params["format.type"].Description,
				Type:        spec.Params["format.type"].Type,
				Default:     "structured",
				Validations: spec.Params["format.type"].Validations,
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
				Description: spec.Params["rate"].Description,
				Type:        spec.Params["rate"].Type,
				Default:     "1",
			},
		},
	}
}

// getDemoDestinationFileSpec returns a simplified version of the destination file connector.
func (c *InitCommand) getDemoDestinationFileSpec(spec connectorSpec) connectorSpec {
	return connectorSpec{
		Name: defaultDestination,
		Params: map[string]config.Parameter{
			"path": {
				Description: spec.Params["path"].Description,
				Type:        spec.Params["path"].Type,
				Default:     "./destination.txt",
			},
		},
	}
}

func (c *InitCommand) buildTemplatePipeline() (pipelineTemplate, error) {
	srcSpec, err := c.getSourceSpec()
	if err != nil {
		return pipelineTemplate{}, cerrors.Errorf("failed getting source params: %w", err)
	}

	// provide a simplified version
	if c.flags.Source == "" {
		srcSpec = c.getDemoSourceGeneratorSpec(srcSpec)
	}

	dstSpec, err := c.getDestinationSpec()
	if err != nil {
		return pipelineTemplate{}, cerrors.Errorf("failed getting destination params: %w", err)
	}

	// provide a simplified version
	if c.flags.Destination == "" {
		dstSpec = c.getDemoDestinationFileSpec(dstSpec)
	}

	return pipelineTemplate{
		Name:            c.getPipelineName(),
		SourceSpec:      srcSpec,
		DestinationSpec: dstSpec,
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

// getPipelineName returns the desired pipeline name based on configuration.
// If user provided one, it'll respect it. Otherwise, it'll be based on source and dest connectors.
func (c *InitCommand) getPipelineName() string {
	src := defaultSource
	dest := defaultDestination

	if c.args.pipelineName != defaultPipelineName {
		return c.args.pipelineName
	}

	if c.flags.Source != "" {
		src = c.flags.Source
	}

	if c.flags.Destination != "" {
		dest = c.flags.Destination
	}
	return fmt.Sprintf("%s-to-%s", src, dest)
}

func (c *InitCommand) Execute(_ context.Context) error {
	c.configFilePath = filepath.Join(c.flags.PipelinesPath, fmt.Sprintf("pipeline-%s.yaml", c.getPipelineName()))

	pipeline, err := c.buildTemplatePipeline()
	if err != nil {
		return err
	}

	if err := c.write(pipeline); err != nil {
		return cerrors.Errorf("could not write pipeline: %w", err)
	}

	fmt.Printf("Your pipeline has been initialized and created at %q.\n"+
		"To run the pipeline, simply run `conduit run`.\n", c.configFilePath)

	return nil
}
