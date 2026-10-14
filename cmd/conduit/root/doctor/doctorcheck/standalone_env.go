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
// only when at least one non-directory file is present. That is slightly
// broader than the startup warning, which counts plugins that loaded
// successfully: a stray non-plugin file in connectors.path makes doctor warn
// where the engine would stay quiet. Doctor never executes plugins here.
//
// When redact is set (the MCP tool, whose result reaches a model provider and,
// over HTTP, every bearer-token holder), the finding carries only a count. The
// full names stay in the local CLI doctor and in the engine's startup log.
type standaloneEnvCheck struct {
	connectorsDir string
	enabled       bool
	redact        bool
	// environ returns the environment to classify; nil means os.Environ.
	environ func() []string
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

	environ := os.Environ
	if c.environ != nil {
		environ = c.environ
	}
	found := standalone.ClassifyEnv(environ())
	if found.Total() == 0 {
		return check.CheckResult{
			Status:     check.StatusPass,
			Category:   CategoryPlugins,
			ConfigPath: configKeyStandaloneEnv,
			Message:    "no sensitive host environment variables will be passed to standalone plugins",
		}
	}

	suggestion := "install only trusted plugins, or start Conduit with a minimal environment; " +
		"after reviewing the list, set " + configKeyStandaloneEnv + "=false to silence this (see " + standalone.EnvWarningDocsPath + ")"

	if c.redact {
		return check.CheckResult{
			Status:     check.StatusWarn,
			Category:   CategoryPlugins,
			Code:       standalone.CodeEnvPassedToPlugins.Reason(),
			ConfigPath: configKeyStandaloneEnv,
			Message: fmt.Sprintf("%d host environment variable(s) with sensitive-looking names will be passed to standalone plugin processes "+
				"(names withheld from this interface; run 'conduit doctor' locally for names; "+
				"this is the environment of this process, which may differ from the engine's); "+
				"a future release will restrict the plugin environment to an allowlist", found.Total()),
			Suggestion: suggestion,
		}
	}

	list := strings.Join(found.Names, ", ")
	if extra := found.NonStandardSummary(); extra != "" {
		if list != "" {
			list += " and "
		}
		list += extra
	}
	return check.CheckResult{
		Status:     check.StatusWarn,
		Category:   CategoryPlugins,
		Code:       standalone.CodeEnvPassedToPlugins.Reason(),
		ConfigPath: configKeyStandaloneEnv,
		Message: fmt.Sprintf("%d host environment variable(s) will be passed to standalone plugin processes: %s; "+
			"a future release will restrict the plugin environment to an allowlist", found.Total(), list),
		Suggestion: suggestion,
	}
}
