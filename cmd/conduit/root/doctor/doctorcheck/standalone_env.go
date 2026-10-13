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
	"strings"

	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/plugin/connector/standalone"
)

// standaloneEnvCheck is doctor's counterpart of the startup warning logged by
// the standalone connector registry: it reports the NAMES (never values) of
// host environment variables that standalone plugin processes inherit and that
// look like credentials. It is advisory (Warn, never Fail) and executes nothing.
//
// The check reads the directory the same way pluginsDirCheck does and reports
// only when at least one plugin binary candidate is present, mirroring the
// "standalone plugins enabled and installed" condition of the startup warning.
type standaloneEnvCheck struct {
	connectorsDir string
	enabled       bool
}

func (c standaloneEnvCheck) Name() string { return NamePluginsStandaloneEnv }

func (c standaloneEnvCheck) Run(context.Context) check.CheckResult {
	if !c.enabled {
		return check.CheckResult{
			Status:     check.StatusPass,
			Category:   CategoryPlugins,
			ConfigPath: configKeyStandaloneEnv,
			Message:    "standalone plugin environment warning is disabled",
		}
	}

	entries, err := os.ReadDir(c.connectorsDir)
	if err != nil {
		// A missing or unreadable directory is plugins.connectors_dir's finding.
		return check.CheckResult{
			Status:     check.StatusPass,
			Category:   CategoryPlugins,
			ConfigPath: configKeyConnectorsPath,
			Message:    "no standalone connector plugins installed",
		}
	}
	files := 0
	for _, e := range entries {
		if !e.IsDir() {
			files++
		}
	}
	if files == 0 {
		return check.CheckResult{
			Status:     check.StatusPass,
			Category:   CategoryPlugins,
			ConfigPath: configKeyConnectorsPath,
			Message:    "no standalone connector plugins installed",
		}
	}

	names := standalone.SensitiveHostEnvNames()
	if len(names) == 0 {
		return check.CheckResult{
			Status:     check.StatusPass,
			Category:   CategoryPlugins,
			ConfigPath: configKeyStandaloneEnv,
			Message:    "no sensitive host environment variables will be passed to standalone plugins",
		}
	}

	return check.CheckResult{
		Status:     check.StatusWarn,
		Category:   CategoryPlugins,
		Code:       standalone.CodeEnvPassedToPlugins.Reason(),
		ConfigPath: configKeyStandaloneEnv,
		Message: fmt.Sprintf("%d host environment variable(s) will be passed to standalone plugin processes: %s; "+
			"v0.21 will restrict the plugin environment to an allowlist", len(names), strings.Join(names, ", ")),
		Suggestion: "install only trusted plugins, or start Conduit with a minimal environment; " +
			"after reviewing the list, set " + configKeyStandaloneEnv + "=false to silence this (see " + standalone.EnvWarningDocsPath + ")",
	}
}
