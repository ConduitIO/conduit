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

package standalone

import (
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/conduitio/conduit-connector-protocol/pconnector/client"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc/codes"
)

// EnvAllowConfigPath is the operator config key listing the extra host
// environment variables standalone connector plugins receive.
const EnvAllowConfigPath = "connectors.standalone.env.allow"

// CodeEnvStripped is the stable code on the startup log event that lists the
// host environment variable names standalone plugins no longer receive. Like
// CodeEnvPassedToPlugins it is a log/finding code, not an API error code.
var CodeEnvStripped = conduiterr.Register("plugin.standalone_env_stripped", codes.FailedPrecondition)

// baseEnvNames are the host variables every standalone plugin gets, and why:
//
//   - PATH, HOME: plugins that exec helpers or resolve ~ (every OS).
//   - TMPDIR (Unix), TEMP, TMP (Windows): os.TempDir, which go-plugin uses for
//     its unix socket directory.
//   - TZ, LANG, LANGUAGE, LC_*: time zone and locale, so formatting and
//     collation match what the operator sees.
//
// On Windows also SYSTEMROOT and WINDIR: the Go runtime and Winsock fail to
// initialise without SYSTEMROOT.
//
// The handshake needs nothing from the host: go-plugin appends the magic
// cookie, PLUGIN_MIN_PORT, PLUGIN_MAX_PORT, PLUGIN_PROTOCOL_VERSIONS (and
// PLUGIN_CLIENT_CERT with AutoMTLS) after the base environment, and the
// registry sets the CONDUIT_CONNECTOR_* variables the SDK reads. A test starts
// a real plugin with exactly this environment.
const (
	envPath     = "PATH"
	envHome     = "HOME"
	envLang     = "LANG"
	goosWindows = "windows"
)

var baseEnvNames = []string{envPath, envHome, "TMPDIR", "TEMP", "TMP", "TZ", envLang, "LANGUAGE"}

var windowsEnvNames = []string{"SYSTEMROOT", "WINDIR"}

// reservedEnvPrefixes are never passed from the host even when allowlisted: the
// registry owns them (a host value would shadow Conduit's, since the plugin
// sees the first match on some platforms), and PLUGIN_* is the go-plugin
// handshake.
var reservedEnvPrefixes = []string{"CONDUIT_CONNECTOR_", "PLUGIN_"}

// EnvPolicy decides which host variables a standalone plugin receives.
type EnvPolicy struct {
	allow map[string]struct{}
	goos  string
}

// NewEnvPolicy validates the operator's allowlist (exact names, no patterns)
// and returns the policy. Entries may be comma or space separated.
func NewEnvPolicy(allow []string) (EnvPolicy, error) {
	p := EnvPolicy{allow: map[string]struct{}{}, goos: runtime.GOOS}
	for _, e := range allow {
		for _, n := range strings.FieldsFunc(e, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
			if !validEnvName(n) {
				ce := conduiterr.New(conduiterr.CodeInvalidArgument,
					fmt.Sprintf("%q config value %q is not a valid environment variable name", EnvAllowConfigPath, n))
				ce.ConfigPath = EnvAllowConfigPath
				ce.Suggestion = "list exact variable names ([A-Za-z_][A-Za-z0-9_]*); wildcards and prefixes are not supported (see " + EnvWarningDocsPath + ")"
				return EnvPolicy{}, ce
			}
			for _, pre := range reservedEnvPrefixes {
				if strings.HasPrefix(strings.ToUpper(n), pre) {
					ce := conduiterr.New(conduiterr.CodeInvalidArgument,
						fmt.Sprintf("%q config value %q is reserved: Conduit sets %s* variables itself", EnvAllowConfigPath, n, pre))
					ce.ConfigPath = EnvAllowConfigPath
					ce.Suggestion = "remove the entry"
					return EnvPolicy{}, ce
				}
			}
			p.allow[p.fold(n)] = struct{}{}
		}
	}
	return p, nil
}

func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// fold normalizes a name for comparison: Windows names are case-insensitive.
func (p EnvPolicy) fold(n string) string {
	if p.goos == goosWindows {
		return strings.ToUpper(n)
	}
	return n
}

// passes reports whether the host variable name is passed to plugins.
func (p EnvPolicy) passes(name string) bool {
	for _, pre := range reservedEnvPrefixes {
		if strings.HasPrefix(strings.ToUpper(name), pre) {
			return false
		}
	}
	f := p.fold(name)
	if _, ok := p.allow[f]; ok {
		return true
	}
	for _, b := range baseEnvNames {
		if f == p.fold(b) {
			return true
		}
	}
	if strings.HasPrefix(strings.ToUpper(name), "LC_") {
		return true
	}
	if p.goos == goosWindows {
		for _, b := range windowsEnvNames {
			if f == p.fold(b) {
				return true
			}
		}
	}
	return false
}

// Filter splits environ (KEY=VALUE entries) into the entries passed to plugins
// and the sorted names stripped. Names that are not plain identifiers are
// never passed and are counted, not listed.
func (p EnvPolicy) Filter(environ []string) (passed []string, stripped []string, strippedNonStandard int) {
	seen := map[string]struct{}{}
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			continue // Windows keeps entries such as "=C:=C:\dir"
		}
		if validEnvName(name) && p.passes(name) {
			passed = append(passed, kv)
			continue
		}
		if !standardNameRE.MatchString(name) {
			strippedNonStandard++
			continue
		}
		if _, dup := seen[name]; !dup {
			seen[name] = struct{}{}
			stripped = append(stripped, name)
		}
	}
	sort.Strings(stripped)
	return passed, stripped, strippedNonStandard
}

// option is the client option that replaces the plugin's environment. It must
// be applied BEFORE client.WithEnvVar options: those append to Cmd.Env.
type envOption struct{ env []string }

// ApplyOption implements client.Option.
func (o envOption) ApplyOption(cfg *plugin.ClientConfig) error {
	if cfg.Cmd == nil {
		return fmt.Errorf("plugin client config has no Cmd set")
	}
	// Invariant (security boundary): go-plugin appends os.Environ() to Cmd.Env
	// unless SkipHostEnv is set, and the protocol client sets Cmd.Env to an
	// empty slice, which does not prevent that. SkipHostEnv is the switch.
	cfg.SkipHostEnv = true
	cfg.Cmd.Env = append([]string(nil), o.env...)
	return nil
}

// ClientOption returns the client option that gives a plugin process exactly
// the filtered host environment (plus what go-plugin and the registry add).
func (p EnvPolicy) ClientOption(environ []string) client.Option {
	passed, _, _ := p.Filter(environ)
	return envOption{env: passed}
}
