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

	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithExecute = (*ConfigCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*ConfigCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*ConfigCommand)(nil)
	_ ecdysis.CommandWithConfig  = (*ConfigCommand)(nil)
)

type ConfigCommand struct {
	rootCmd *RootCommand
}

func (c *ConfigCommand) Config() ecdysis.Config {
	return c.rootCmd.Config()
}

func (c *ConfigCommand) Flags() []ecdysis.Flag {
	return c.rootCmd.Flags()
}

func (c *ConfigCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Shows the Conduit Configuration to be used when running conduit",
		Long: `Conduit will run based on the default configuration jointly with a provided configuration file (optional), 
the set environment variables, and the flags used. This command will show the configuration that will be used. `,
	}
}

func (c *ConfigCommand) Usage() string { return "config" }

func (c ConfigCommand) Execute(_ context.Context) error {
	fmt.Print(c.rootCmd.cfg.DB.Postgres.Table)
	return nil
}
