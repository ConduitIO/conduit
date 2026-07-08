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

package processors

import (
	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/scaffoldcmd"
	"github.com/conduitio/conduit/pkg/scaffold"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithResult = (*NewCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*NewCommand)(nil)
	_ ecdysis.CommandWithFlags   = (*NewCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*NewCommand)(nil)
)

// NewCommand implements `conduit processor new`. The command body is shared
// with `conduit connector new` (see cmd/conduit/internal/scaffoldcmd) —
// this type only supplies the processor-specific Usage/Docs.
type NewCommand struct {
	scaffoldcmd.NewCommand
}

func newNewCommand() *NewCommand {
	c := &NewCommand{}
	c.Kind = scaffold.KindProcessor
	return c
}

func (c *NewCommand) Usage() string { return "new [name]" }

func (c *NewCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Scaffold a new Go processor",
		Long: `Scaffold a full processor repository — SDK wiring, tests, CI, release
workflow — from ConduitIO/conduit-processor-template, ready to build with no edits.

The command orchestrates the canonical template rather than reimplementing it: it
renames the module path and package name, installs paramgen, runs code generation
(paramgen_proc.go — not conn-sdk-cli specgen, which the connector template uses;
see the design doc's connector/processor asymmetry note), verifies "go build ./..."
succeeds, and initializes git. Only --lang go is available today.`,
		Example: scaffoldcmd.Example(scaffold.KindProcessor, "uppercase"),
	}
}
