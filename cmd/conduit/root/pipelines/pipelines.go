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
	"github.com/conduitio/ecdysis"
)

// envPrefix is the environment variable prefix every subcommand's
// ecdysis.Config uses (CONDUIT_DB_TYPE, CONDUIT_PIPELINES_PATH, ...) — a
// shared constant so deploy/apply/dev can't drift on it independently.
const envPrefix = "CONDUIT"

var (
	_ ecdysis.CommandWithDocs        = (*PipelinesCommand)(nil)
	_ ecdysis.CommandWithSubCommands = (*PipelinesCommand)(nil)
	_ ecdysis.CommandWithAliases     = (*PipelinesCommand)(nil)
)

type PipelinesCommand struct{}

func (c *PipelinesCommand) Aliases() []string { return []string{"pipeline"} }

func (c *PipelinesCommand) SubCommands() []ecdysis.Command {
	return []ecdysis.Command{
		&InitCommand{},
		&ListCommand{},
		&DescribeCommand{},
		&InspectCommand{},
		&ValidateCommand{},
		&LintCommand{},
		&DryRunCommand{},
		&DeployCommand{},
		&ApplyCommand{},
		&RepairCommand{},
		&StartCommand{},
		&StopCommand{},
		&DevCommand{},
	}
}

func (c *PipelinesCommand) Usage() string { return "pipelines" }

func (c *PipelinesCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Initialize and manage pipelines",
	}
}
