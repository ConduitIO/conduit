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

package root_test

import (
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/root/doctor"
	"github.com/conduitio/conduit/cmd/conduit/root/mcp"
	"github.com/conduitio/conduit/cmd/conduit/root/pipelines"
	"github.com/conduitio/conduit/cmd/conduit/root/run"
	"github.com/conduitio/ecdysis"
	"github.com/matryer/is"
)

// TestStandaloneEnvWarning_FlagDefaultTrue guards a pitfall: a bool flag whose
// config default is true parses as false unless the command calls
// flags.SetDefault. Each command that builds a runtime and spawns standalone
// plugins must therefore default connectors.standalone.env-warning to true when
// no flag is passed, or the startup warning silently never fires.
func TestStandaloneEnvWarning_FlagDefaultTrue(t *testing.T) {
	const flagName = "connectors.standalone.env-warning"

	cmds := map[string]ecdysis.Command{
		"run":              &run.RunCommand{},
		"doctor":           &doctor.DoctorCommand{},
		"mcp":              &mcp.MCPCommand{},
		"pipelines dev":    &pipelines.DevCommand{},
		"pipelines deploy": &pipelines.DeployCommand{},
		"pipelines apply":  &pipelines.ApplyCommand{},
	}
	for name, c := range cmds {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			cobraCmd := ecdysis.New().MustBuildCobraCommand(c)
			is.NoErr(cobraCmd.ParseFlags(nil)) // no args: only defaults apply

			f := cobraCmd.Flags().Lookup(flagName)
			is.True(f != nil)
			is.Equal(f.Value.String(), "true")
		})
	}
}
