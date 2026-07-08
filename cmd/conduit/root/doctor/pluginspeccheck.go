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

package doctor

import (
	"context"
	"fmt"

	"github.com/conduitio/conduit/cmd/conduit/root/doctor/doctorcheck"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/ecdysis"
)

var (
	_ ecdysis.CommandWithExecute = (*PluginSpecCheckCommand)(nil)
	_ ecdysis.CommandWithHidden  = (*PluginSpecCheckCommand)(nil)
	_ ecdysis.CommandWithArgs    = (*PluginSpecCheckCommand)(nil)
	_ ecdysis.CommandWithDocs    = (*PluginSpecCheckCommand)(nil)
)

// PluginSpecCheckCommand is `conduit __plugin-spec-check <path>`: the
// isolated-subprocess worker doctor's (--deep) plugins.standalone_compat
// check re-execs itself as, so a broken or malicious standalone connector
// plugin binary can only ever crash this short-lived worker process, never
// `conduit doctor` itself. See doctorcheck.standaloneCompatCheck's doc for
// the full reasoning (a background-goroutine panic inside hashicorp/go-plugin
// cannot be recover()'d by the calling process).
//
// This is not a documented CLI surface: Hidden() is true, and running it
// directly has no supported use beyond what doctor already does with it.
type PluginSpecCheckCommand struct {
	path string
}

func (c *PluginSpecCheckCommand) Usage() string { return doctorcheck.PluginSpecCheckArg + " <path>" }

func (c *PluginSpecCheckCommand) Hidden() bool { return true }

func (c *PluginSpecCheckCommand) Args(args []string) error {
	if len(args) != 1 {
		return cerrors.Errorf("expected exactly one argument (the plugin binary path), got %d", len(args))
	}
	c.path = args[0]
	return nil
}

func (c *PluginSpecCheckCommand) Execute(ctx context.Context) error {
	if err := doctorcheck.CheckConnectorPluginSpec(ctx, c.path); err != nil {
		return fmt.Errorf("plugin spec check failed for %s: %w", c.path, err)
	}
	return nil
}

func (c *PluginSpecCheckCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "internal: check a standalone connector plugin's specification in an isolated process (used by `doctor --deep`)",
	}
}
