// Copyright © 2025 Meroxa, Inc.
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

package processors

import (
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithDocs        = (*ProcessorsCommand)(nil)
	_ ecdysis.CommandWithSubCommands = (*ProcessorsCommand)(nil)
	_ ecdysis.CommandWithAliases     = (*ProcessorsCommand)(nil)
)

type ProcessorsCommand struct{}

func (c *ProcessorsCommand) Aliases() []string { return []string{"processor"} }

func (c *ProcessorsCommand) SubCommands() []ecdysis.Command {
	return []ecdysis.Command{
		&ListCommand{},
		&DescribeCommand{},
	}
}

func (c *ProcessorsCommand) Usage() string { return "processors" }

func (c *ProcessorsCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Manage Processors",
	}
}
