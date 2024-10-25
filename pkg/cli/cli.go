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
	"fmt"
	"os"
	"slices"

	"github.com/spf13/cobra"
)

var (
	initArgs = InitArgs{
		Path: ".",
	}
	pipelinesInitArgs = PipelinesInitArgs{
		Name: "example-pipeline",
		Path: "./pipelines/generator-to-log.yaml",
	}
)

type Instance struct {
	rootCmd *cobra.Command
}

func New() *Instance {
	return &Instance{
		rootCmd: buildRootCmd(),
	}
}

// ShouldRun checks if the CLI should be enabled by
// checking if the first command is a known command.
func (i *Instance) ShouldRun() bool {
	if len(os.Args) == 0 {
		return false
	}

	cmd := os.Args[1]
	for _, sub := range i.rootCmd.Commands() {
		if sub.Name() == cmd || slices.Contains(sub.Aliases, cmd) {
			return true
		}
	}

	return false
}

func (i *Instance) Run() {
	if err := i.rootCmd.Execute(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func (i *Instance) Usage() string {
	return i.rootCmd.UsageString()
}

func buildRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "conduit",
		Short: "Conduit CLI",
	}

	cmd.AddCommand(buildInitCmd())
	cmd.AddCommand(buildPipelinesCmd())

	return cmd
}

func buildInitCmd() *cobra.Command {
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize Conduit with a configuration file and directories.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return ConduitInit{Args: initArgs}.Run()
		},
	}
	initCmd.Flags().StringVar(
		&initArgs.Path,
		"path",
		"",
		"path where Conduit will be initialized",
	)

	return initCmd
}

func buildPipelinesCmd() *cobra.Command {
	pipelinesCmd := &cobra.Command{
		Use:   "pipelines",
		Short: "Manage pipelines",
		Args:  cobra.NoArgs,
	}

	pipelinesCmd.AddCommand(buildPipelinesInitCmd())

	return pipelinesCmd
}

func buildPipelinesInitCmd() *cobra.Command {
	pipelinesInitCmd := &cobra.Command{
		Use:   "init [pipeline-name]",
		Short: "Initialize an example pipeline.",
		Long: "Initialize an example pipeline. If a source or destination connector is specified, all of its parameters" +
			" and their descriptions, types and default values are shown.",
		Args:    cobra.MaximumNArgs(1),
		Example: "  conduit pipelines init my-pipeline-name --source postgres --destination kafka --path pipelines/pg-to-kafka.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				pipelinesInitArgs.Name = args[0]
			}
			return PipelinesInit{Args: pipelinesInitArgs}.Run()
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
		"path",
		"",
		"Path where the pipeline will be saved. If no path is specified, prints to stdout.",
	)

	return pipelinesInitCmd
}
