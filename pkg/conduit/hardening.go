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
	"github.com/conduitio/conduit/pkg/foundation/env"
	"github.com/conduitio/conduit/pkg/foundation/pathpolicy"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/custom"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
)

// Config keys of the settings-hardening controls (GHSA-m3mw-fpw8-w765).
const (
	EnvAllowKey              = env.AllowConfigPath
	FileAllowedDirsKey       = "connectors.file.allowed-dirs"
	JavaScriptAllowedDirsKey = "processors.javascript.allowed-dirs"
	BuiltinEgressAllowKey    = egress.BuiltinAllowConfigPath
)

// EnvAllowlist returns the operator's env.allow list. A malformed name fails
// startup; an empty list allows no ${VAR} expansion at all.
func (c Config) EnvAllowlist() (*env.Allowlist, error) {
	return env.NewAllowlist(c.Env.Allow)
}

// fileConnectorPolicy returns the directories the built-in file connector is
// confined to.
func (c Config) fileConnectorPolicy() (*pathpolicy.Policy, error) {
	return pathpolicy.New(FileAllowedDirsKey, c.Connectors.File.AllowedDirs)
}

// SecretsAllowedDirsKey is the operator key for TLS and credential files named
// by connector settings.
const SecretsAllowedDirsKey = "secrets.allowed-dirs"

// systemCertDirs are the conventional locations of system CA bundles and
// container secrets, allowed for cert material by default. They are read-only
// public trust anchors on a normal host; a Kubernetes service account token
// directory is deliberately not among them.
var systemCertDirs = []string{
	"/etc/ssl", "/etc/pki", "/etc/ca-certificates", "/usr/share/ca-certificates",
	"/usr/local/share/ca-certificates", "/run/secrets",
}

// secretsPolicy returns the directories TLS and credential files named by
// connector settings may be read from.
func (c Config) secretsPolicy() (*pathpolicy.Policy, error) {
	return pathpolicy.New(SecretsAllowedDirsKey, c.Secrets.AllowedDirs)
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

// validateHardeningConfig fails startup on a malformed env.allow, allowed-dirs
// or builtin-allow entry. It never silently drops an entry.
func (c Config) validateHardeningConfig() error {
	if _, err := c.EnvAllowlist(); err != nil {
		return err //nolint:wrapcheck // coded error naming the config key
	}
	if _, err := c.fileConnectorPolicy(); err != nil {
		return cerrors.Errorf("invalid %s: %w", FileAllowedDirsKey, err)
	}
	if _, err := c.secretsPolicy(); err != nil {
		return cerrors.Errorf("invalid %s: %w", SecretsAllowedDirsKey, err)
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
// installs. It expands ${VAR} only for the operator's env.allow names and
// confines every setting that names a file (see fileRefs) to the operator's
// directories: data files to connectors.file.allowed-dirs, TLS and credential
// material to secrets.allowed-dirs.
type engineSettingsResolver struct {
	env     *env.Allowlist
	files   *pathpolicy.Policy
	secrets *pathpolicy.Policy
}

var _ connector.SettingsResolver = engineSettingsResolver{}

func (r engineSettingsResolver) policyFor(ref fileRef) *pathpolicy.Policy {
	if ref.secret {
		return r.secrets
	}
	return r.files
}

// ResolveSettings implements connector.SettingsResolver.
func (r engineSettingsResolver) ResolveSettings(pluginName string, settings map[string]string) (map[string]string, error) {
	out, err := env.ResolveSettings(r.env, settings)
	if err != nil {
		return nil, err //nolint:wrapcheck // coded error with config path
	}
	// Tier 1 (configure path): the check runs on the EXPANDED value so
	// ${DIR}/x cannot dodge it, and the resolved (symlink-free) path replaces
	// the supplied one so the plugin opens the path that was checked.
	for _, ref := range fileRefs(pluginName, out) {
		resolved, err := r.policyFor(ref).Check("settings."+ref.key, ref.path)
		if err != nil {
			return nil, err //nolint:wrapcheck // coded error with config path
		}
		if ref.set != nil {
			ref.set(out, resolved)
		}
	}
	return out, nil
}

// CheckSettings is the connector.PluginService settings guard for validation
// requests. It applies the same confinement to literal paths so a validation
// call cannot probe files outside the allowed directories. A value that still
// contains a "$" reference is skipped here and checked after expansion when the
// connector is configured.
func (r engineSettingsResolver) CheckSettings(pluginName string, settings map[string]string) error {
	for _, ref := range fileRefs(pluginName, settings) {
		if strings.Contains(ref.path, "$") {
			continue
		}
		if _, err := r.policyFor(ref).Check("settings."+ref.key, ref.path); err != nil {
			return err //nolint:wrapcheck // coded error with config path
		}
	}
	return nil
}
