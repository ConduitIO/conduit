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

	"github.com/conduitio/conduit/cmd/conduit/global"
	"github.com/conduitio/conduit/cmd/conduit/root/version"
	"github.com/conduitio/ecdysis"
)

type RootFlags struct {
	Config  string `long:"config" usage:"config file (default is $HOME/.conduit.yaml)" persistent:"true"`
	Author  string `long:"author" short:"a" usage:"author name for copyright attribution" persistent:"true"`
	License string `long:"license" short:"l" usage:"name of license for the project" persistent:"true"`
	Viper   bool   `long:"viper" usage:"use Viper for configuration" persistent:"true"`
}

type RootCommand struct {
	flags RootFlags
}

func (c *RootCommand) Execute(ctx context.Context) error {
	global.New().Run()
	return nil
}

var (
	_ ecdysis.CommandWithFlags       = (*RootCommand)(nil)
	_ ecdysis.CommandWithDocs        = (*RootCommand)(nil)
	_ ecdysis.CommandWithSubCommands = (*RootCommand)(nil)
	_ ecdysis.CommandWithExecute     = (*RootCommand)(nil)
)

func (c *RootCommand) Usage() string { return "conduit" }
func (c *RootCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)

	flags.SetDefault("author", "YOUR NAME")
	flags.SetDefault("viper", true)
	return flags
}

func (c *RootCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Conduit CLI",
		Long:  `Conduit CLI is a command-line that helps you interact with and manage Conduit.`,
	}
}

func (c *RootCommand) SubCommands() []ecdysis.Command {
	return []ecdysis.Command{
		&version.VersionCommand{},
	}
}
