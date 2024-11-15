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

package global

import (
	"fmt"
	"os"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	initArgs          InitArgs
	pipelinesInitArgs PipelinesInitArgs
)

type Instance struct {
	rootCmd *cobra.Command
}

// New creates a new CLI Instance.
func New() *Instance {
	return &Instance{
		rootCmd: buildRootCmd(),
	}
}

func (i *Instance) Run() {
	if err := i.rootCmd.Execute(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func buildRootCmd() *cobra.Command {
	cfg := conduit.DefaultConfig()

	cmd := &cobra.Command{
		Use:     "conduit",
		Short:   "Conduit CLI",
		Long:    "Conduit CLI is a command-line that helps you interact with and manage Conduit.",
		Version: conduit.Version(true),
		Run: func(cmd *cobra.Command, args []string) {
			e := &conduit.Entrypoint{}
			e.Serve(cfg)
		},
	}
	cmd.CompletionOptions.DisableDefaultCmd = true
	conduit.Flags(&cfg).VisitAll(cmd.Flags().AddGoFlag)

	// init
	cmd.AddCommand(buildInitCmd())

	// pipelines
	cmd.AddGroup(&cobra.Group{
		ID:    "pipelines",
		Title: "Pipelines",
	})
	cmd.AddCommand(buildPipelinesCmd())

	// mark hidden flags
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if conduit.HiddenFlags[f.Name] {
			err := cmd.Flags().MarkHidden(f.Name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to mark flag %q as hidden: %v", f.Name, err)
			}
		}
	})

	return cmd
}

func buildInitCmd() *cobra.Command {
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize Conduit with a configuration file and directories.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return NewConduitInit(initArgs).Run()
		},
	}
	initCmd.Flags().StringVar(
		&initArgs.Path,
		"config.path",
		"",
		"path where Conduit will be initialized",
	)

	return initCmd
}

func buildPipelinesCmd() *cobra.Command {
	pipelinesCmd := &cobra.Command{
		Use:     "pipelines",
		Short:   "Initialize and manage pipelines",
		Args:    cobra.NoArgs,
		GroupID: "pipelines",
	}

	pipelinesCmd.AddCommand(buildPipelinesInitCmd())

	return pipelinesCmd
}

func buildPipelinesInitCmd() *cobra.Command {
	pipelinesInitCmd := &cobra.Command{
		Use:   "init [pipeline-name]",
		Short: "Initialize an example pipeline.",
		Long: `Initialize a pipeline configuration file, with all of parameters for source and destination connectors 
initialized and described. The source and destination connector can be chosen via flags. If no connectors are chosen, then
a simple and runnable generator-to-log pipeline is configured.`,
		Args:    cobra.MaximumNArgs(1),
		Example: "  conduit pipelines init awesome-pipeline-name --source postgres --destination kafka --path pipelines/pg-to-kafka.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				pipelinesInitArgs.Name = args[0]
			}
			return NewPipelinesInit(pipelinesInitArgs).Run()
		},
	}

	// Add flags to pipelines init command
	pipelinesInitCmd.Flags().StringVar(
		&pipelinesInitArgs.Source,
		"source",
		"",
		"Source connector (any of the built-in connectors).",
	)
	pipelinesInitCmd.Flags().StringVar(
		&pipelinesInitArgs.Destination,
		"destination",
		"",
		"Destination connector (any of the built-in connectors).",
	)
	pipelinesInitCmd.Flags().StringVar(
		&pipelinesInitArgs.Path,
		"pipelines.path",
		"./pipelines",
		"Path where the pipeline will be saved.",
	)

	return pipelinesInitCmd
}
