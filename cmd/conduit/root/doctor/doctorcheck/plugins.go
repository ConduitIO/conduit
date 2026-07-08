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

package doctorcheck

import (
	"context"
	"fmt"
	"os"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// pluginsDirCheck scans a standalone plugin directory the same way
// pkg/plugin/connector/standalone.Registry (and its processor counterpart)
// do — a plain os.ReadDir — without loading anything from it. A missing
// directory is a Warn (built-in plugins still work), not a Fail: doctor
// treats "no standalone plugins configured" as normal, not a problem.
type pluginsDirCheck struct {
	name      string
	path      string
	configKey string
}

func (c pluginsDirCheck) Name() string { return c.name }

func (c pluginsDirCheck) Run(context.Context) check.CheckResult {
	entries, err := os.ReadDir(c.path)
	switch {
	case err == nil:
		files := 0
		for _, e := range entries {
			if !e.IsDir() {
				files++
			}
		}
		return check.CheckResult{
			Status:     check.StatusPass,
			Category:   CategoryPlugins,
			ConfigPath: c.configKey,
			Message:    fmt.Sprintf("%s: %d plugin binary candidate(s) found", c.path, files),
		}
	case cerrors.Is(err, os.ErrNotExist):
		return check.CheckResult{
			Status:     check.StatusWarn,
			Category:   CategoryPlugins,
			ConfigPath: c.configKey,
			Message:    fmt.Sprintf("%s not found — built-in plugins are still available", c.path),
			Suggestion: fmt.Sprintf("create %s if you want to use standalone plugins, or ignore this if you only use built-ins", c.path),
		}
	default:
		// Exists but isn't a readable directory (a plain file, or a
		// permissions problem) — this one IS a problem: `conduit run`'s own
		// plugin scan would hit the same error and silently skip the
		// directory (it only warns to its own log), so surfacing it here
		// as a Fail is doctor adding signal, not duplicating one.
		return check.CheckResult{
			Status:     check.StatusFail,
			Category:   CategoryPlugins,
			Code:       conduiterr.CodeInvalidArgument.Reason(),
			ConfigPath: c.configKey,
			Message:    fmt.Sprintf("cannot read %s: %v", c.path, err),
			Suggestion: fmt.Sprintf("check that %s is a directory and is readable, or point %s elsewhere", c.path, c.configKey),
		}
	}
}

// pluginsBuiltinCheck reports on cfg's own ConnectorPlugins/ProcessorPlugins
// maps (populated from builtin.DefaultBuiltinConnectors /
// proc_builtin.DefaultBuiltinProcessors by conduit.DefaultConfigWithBasePath,
// or by an embedder) rather than re-deriving them from those packages
// directly — cfg is what `conduit run` actually uses, so an embedder that
// overrides these maps gets checked against what it actually configured.
//
// An empty map here means the binary was built without its built-in plugin
// registry wired up, or an embedder cleared it by mistake — a bug, not an
// environment problem, so this maps to exit bucket 1 (Runtime), not 2 or 3.
type pluginsBuiltinCheck struct {
	cfg conduit.Config
}

func (c pluginsBuiltinCheck) Name() string { return NamePluginsBuiltin }

func (c pluginsBuiltinCheck) Run(context.Context) check.CheckResult {
	connectors := len(c.cfg.ConnectorPlugins)
	processors := len(c.cfg.ProcessorPlugins)

	if connectors == 0 && processors == 0 {
		return check.CheckResult{
			Status:   check.StatusFail,
			Category: CategoryPlugins,
			Code:     conduiterr.CodeInternal.Reason(),
			Message:  "no built-in connectors or processors are registered",
			Suggestion: "this should not happen with a stock `conduit` binary — " +
				"if you're embedding Conduit, check that you populated ConnectorPlugins/ProcessorPlugins",
		}
	}

	return check.CheckResult{
		Status:   check.StatusPass,
		Category: CategoryPlugins,
		Message:  fmt.Sprintf("%d built-in connector(s), %d built-in processor(s) available", connectors, processors),
	}
}
