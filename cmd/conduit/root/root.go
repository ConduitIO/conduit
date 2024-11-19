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

package root

import (
	"fmt"
	"os"

	"github.com/conduitio/conduit/cmd/conduit/root/pipelines"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	initArgs InitArgs
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
	initCmd := buildInitCmd()
	cmd.AddCommand(initCmd)

	// pipelines
	cmd.AddGroup(&cobra.Group{
		ID:    "pipelines",
		Title: "Pipelines",
	})
	cmd.AddCommand(pipelines.BuildPipelinesCmd())

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
