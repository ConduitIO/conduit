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
	"context"
	"fmt"
	"os"

	"github.com/conduitio/conduit/cmd/conduit/root/config"
	"github.com/conduitio/conduit/cmd/conduit/root/connectorplugins"
	"github.com/conduitio/conduit/cmd/conduit/root/connectors"
	"github.com/conduitio/conduit/cmd/conduit/root/initialize"
	"github.com/conduitio/conduit/cmd/conduit/root/pipelines"
	"github.com/conduitio/conduit/cmd/conduit/root/run"
	"github.com/conduitio/conduit/cmd/conduit/root/version"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithFlags       = (*RootCommand)(nil)
	_ ecdysis.CommandWithExecute     = (*RootCommand)(nil)
	_ ecdysis.CommandWithDocs        = (*RootCommand)(nil)
	_ ecdysis.CommandWithSubCommands = (*RootCommand)(nil)
)

type RootFlags struct {
	Version bool `long:"version" short:"v" usage:"show the current Conduit version"`

	// Global Flags
	GRPCAddress string `long:"api.grpc.address" usage:"address where Conduit is running" persistent:"true"`
	ConfigPath  string `long:"config.path" usage:"path to the configuration file" persistent:"true"`
}

type RootCommand struct {
	flags RootFlags
}

func (c *RootCommand) Execute(ctx context.Context) error {
	if c.flags.Version {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", conduit.Version(true))
		return nil
	}

	if cmd := ecdysis.CobraCmdFromContext(ctx); cmd != nil {
		return cmd.Help()
	}

	return nil
}

func (c *RootCommand) Usage() string { return "conduit" }

func (c *RootCommand) Flags() []ecdysis.Flag {
	return ecdysis.BuildFlags(&c.flags)
}

func (c *RootCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Conduit CLI",
		Long:  `Conduit CLI is a command-line that helps you interact with and manage Conduit.`,
	}
}

func (c *RootCommand) SubCommands() []ecdysis.Command {
	runCmd := &run.RunCommand{}

	return []ecdysis.Command{
		&config.ConfigCommand{RunCmd: runCmd},
		&initialize.InitCommand{Cfg: &runCmd.Cfg},
		&version.VersionCommand{},
		&pipelines.PipelinesCommand{},
		&connectors.ConnectorsCommand{},
		&connectorplugins.ConnectorPluginsCommand{},
		runCmd,
	}
}
