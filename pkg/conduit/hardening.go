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

package conduit

import (
	"os"
	"strings"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/pathpolicy"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/custom"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
)

// Config keys of the settings-hardening controls (GHSA-m3mw-fpw8-w765).
const (
	FileAllowedDirsKey          = "connectors.file.allowed-dirs"
	JavaScriptAllowedDirsKey    = "processors.javascript.allowed-dirs"
	BuiltinEgressAllowKey       = egress.BuiltinAllowConfigPath
	fileConnectorPluginName     = "file"
	fileConnectorPathSetting    = "path"
	fileConnectorModulePluginID = "conduit-connector-file"
)

// fileConnectorPolicy returns the directories the built-in file connector is
// confined to.
func (c Config) fileConnectorPolicy() (*pathpolicy.Policy, error) {
	return pathpolicy.New(FileAllowedDirsKey, c.Connectors.File.AllowedDirs)
}

// ensureDefaultFilesDir creates <data dir>/files (0700) when it is still one of
// the allowed directories, so the out-of-the-box file connector works without
// setup. Only the default is ever created; a directory the operator names is
// the operator's to create. Failure is not fatal: the connector then reports
// the missing directory itself.
func (c Config) ensureDefaultFilesDir() error {
	d := c.Connectors.File.defaultDir
	if d == "" {
		return nil
	}
	for _, a := range c.Connectors.File.AllowedDirs {
		if a == d {
			return os.MkdirAll(d, 0o700)
		}
	}
	return nil
}

// javascriptFileAccess returns the custom.javascript file policy:
// script.path may read from the explicit allowed dirs, or from the default
// scripts directory when none is set; require() is enabled only for explicit
// allowed dirs.
func (c Config) javascriptFileAccess() (custom.FileAccess, error) {
	explicit := c.Processors.JavaScript.AllowedDirs
	scriptDirs := explicit
	if len(scriptDirs) == 0 && c.Processors.JavaScript.scriptsDir != "" {
		scriptDirs = []string{c.Processors.JavaScript.scriptsDir}
	}
	sp, err := pathpolicy.New(JavaScriptAllowedDirsKey, scriptDirs)
	if err != nil {
		return custom.FileAccess{}, err //nolint:wrapcheck // names the config key
	}
	rp, err := pathpolicy.New(JavaScriptAllowedDirsKey, explicit)
	if err != nil {
		return custom.FileAccess{}, err //nolint:wrapcheck // names the config key
	}
	return custom.FileAccess{ScriptDirs: sp, RequireDirs: rp}, nil
}

// builtinDialGuard returns the dial guard of the built-in outbound-HTTP
// processors.
func (c Config) builtinDialGuard() (*egress.DialGuard, error) {
	allow, err := egress.ParseBuiltinAllow(splitList(c.Processors.Egress.BuiltinAllow))
	if err != nil {
		return nil, cerrors.Errorf("%q config value is invalid: %w", BuiltinEgressAllowKey, err)
	}
	return egress.NewDialGuard(allow), nil
}

// validateHardeningConfig fails startup on a malformed allowed-dirs or
// builtin-allow entry. It never silently drops an entry.
func (c Config) validateHardeningConfig() error {
	if _, err := c.fileConnectorPolicy(); err != nil {
		return cerrors.Errorf("invalid %s: %w", FileAllowedDirsKey, err)
	}
	if _, err := c.javascriptFileAccess(); err != nil {
		return cerrors.Errorf("invalid %s: %w", JavaScriptAllowedDirsKey, err)
	}
	if _, err := c.builtinDialGuard(); err != nil {
		return err
	}
	return nil
}

// engineSettingsResolver is the connector.SettingsResolver the runtime
// installs. It confines the file connector's path setting to the operator's
// directories.
type engineSettingsResolver struct {
	files *pathpolicy.Policy
}

var _ connector.SettingsResolver = engineSettingsResolver{}

// ResolveSettings implements connector.SettingsResolver.
func (r engineSettingsResolver) ResolveSettings(pluginName string, settings map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(settings))
	for k, v := range settings {
		out[k] = v
	}
	// Tier 1 (configure path): the resolved (symlink-free) path replaces the
	// supplied one so the plugin opens the path that was checked.
	if isFileConnector(pluginName) {
		if p, ok := out[fileConnectorPathSetting]; ok {
			resolved, err := r.files.Check("settings."+fileConnectorPathSetting, p)
			if err != nil {
				return nil, err //nolint:wrapcheck // coded error with config path
			}
			out[fileConnectorPathSetting] = resolved
		}
	}
	return out, nil
}

func isFileConnector(pluginName string) bool {
	n := plugin.FullName(pluginName).PluginName()
	return n == fileConnectorPluginName || n == fileConnectorModulePluginID
}

// splitList flattens a repeatable flag / yaml list / comma-separated env value
// into its entries.
func splitList(in []string) []string {
	var out []string
	for _, raw := range in {
		out = append(out, strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n'
		})...)
	}
	return out
}
