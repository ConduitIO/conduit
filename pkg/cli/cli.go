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
	"github.com/spf13/cobra"
	"golang.org/x/exp/slices"
	"os"
)

type InitArgs struct {
	Path string
}

type PipelinesInitArgs struct {
	Name        string
	Source      string
	Destination string
	Path        string
}

var (
	initArgs = InitArgs{
		Path: ".",
	}
	pipelinesInitArgs = PipelinesInitArgs{
		Name: "example-pipeline",
		Path: "./pipelines/generator-to-log.yaml",
	}
	rootCmd *cobra.Command
)

func init() {
	rootCmd = buildRootCmd()
}

func Enabled() bool {
	for _, cmd := range os.Args[1:] {
		for _, sub := range rootCmd.Commands() {
			if sub.Name() == cmd || slices.Contains(sub.Aliases, cmd) {
				return true
			}
		}
	}

	return false
}

func SwitchToCLI() {
	rootCmd := buildRootCmd()
	if err := rootCmd.Execute(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func buildRootCmd() *cobra.Command {
	var rootCmd = &cobra.Command{
		Use:   "conduit",
		Short: "Conuit CLI",
	}

	rootCmd.AddCommand(buildInitCmd())
	rootCmd.AddCommand(buildPipelinesCmd())

	return rootCmd
}

func buildInitCmd() *cobra.Command {
	var initCmd = &cobra.Command{
		Use:   "init",
		Short: "Initialize Conduit with a configuration file and directories.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return ConduitInit{Path: initArgs.Path}.Run()
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
	var pipelinesCmd = &cobra.Command{
		Use:   "pipelines",
		Short: "Manage pipelines",
		Args:  cobra.NoArgs,
	}

	pipelinesCmd.AddCommand(buildPipelinesInitCmd())

	return pipelinesCmd
}

func buildPipelinesInitCmd() *cobra.Command {
	var pipelinesInitCmd = &cobra.Command{
		Use:   "init [pipeline-name]",
		Short: "Initializes an example pipeline.",
		Long: "Initializes an example pipeline. If a source or destination connector is specified, all of its parameters" +
			" and their descriptions, types and default values are shown.",
		Args:    cobra.MaximumNArgs(1),
		Example: "  conduit pipelines init my-pipeline-name --source postgres --destination kafka --path pipelines/pg-to-kafka.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				pipelinesInitArgs.Name = args[0]
			}
			return PipelinesInit{
				Name:        pipelinesInitArgs.Name,
				Source:      pipelinesInitArgs.Source,
				Destination: pipelinesInitArgs.Destination,
				Path:        pipelinesInitArgs.Path,
			}.Run()
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
