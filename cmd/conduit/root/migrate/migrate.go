// Copyright © 2026 Meroxa, Inc.
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

// Package migrate holds `conduit migrate`, whose one subcommand,
// `kafka-connect`, turns Kafka Connect connector configs into Conduit
// pipelines plus a compatibility report. The engine is pkg/kcmigrate; this
// package reads the inputs, writes the outputs and renders the result.
package migrate

import (
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithDocs        = (*MigrateCommand)(nil)
	_ ecdysis.CommandWithSubCommands = (*MigrateCommand)(nil)
	_ ecdysis.CommandWithHidden      = (*MigrateCommand)(nil)
)

// MigrateCommand is the `conduit migrate` group.
type MigrateCommand struct{}

func (c *MigrateCommand) Usage() string { return "migrate" }

func (c *MigrateCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{Short: "Migrate connectors from other systems to Conduit pipelines"}
}

func (c *MigrateCommand) SubCommands() []ecdysis.Command {
	return []ecdysis.Command{&KafkaConnectCommand{}}
}

// Hidden keeps the group out of help until the first connector family's
// rules ship: until then every class is reported unsupported, and a listed
// command that migrates nothing would mislead. It runs when invoked.
func (c *MigrateCommand) Hidden() bool { return true }
