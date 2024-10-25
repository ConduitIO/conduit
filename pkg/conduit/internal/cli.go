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

package internal

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

type InitArgs struct {
	Path string
}

type PipelinesInitArgs struct {
	Source      string
	Destination string
	Path        string
}

var (
	initArgs = InitArgs{
		Path: ".",
	}
	pipelinesInitArgs = PipelinesInitArgs{
		Path: "./pipelines/generator-to-log.yaml",
	}
)

func SwitchToCLI() {
	rootCmd := cmd()
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func cmd() *cobra.Command {
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
	initCmd.Flags().StringVar(&initArgs.Path, "path", "", "path where Conduit will be initialized")

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
		Use:   "init",
		Short: "Initialize pipeline",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pipelineName := ""
			if len(args) > 0 {
				pipelineName = args[0]
			}
			return PipelinesInit{
				Name:        pipelineName,
				Source:      pipelinesInitArgs.Source,
				Destination: pipelinesInitArgs.Destination,
				Path:        pipelinesInitArgs.Path,
			}.Run()
		},
	}

	// Add flags to pipelines init command
	pipelinesInitCmd.Flags().StringVar(&pipelinesInitArgs.Source, "source", "", "Specify the source")
	pipelinesInitCmd.Flags().StringVar(&pipelinesInitArgs.Destination, "destination", "", "Specify the destination")
	pipelinesInitCmd.Flags().StringVar(&pipelinesInitArgs.Path, "path", "", "Specify the path")

	return pipelinesInitCmd
}
