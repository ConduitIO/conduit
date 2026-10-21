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
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/env"
	"github.com/conduitio/conduit/pkg/foundation/pathpolicy"
	"github.com/matryer/is"
)

func TestDefaultConfig_SettingsHardeningDefaults(t *testing.T) {
	is := is.New(t)
	base := t.TempDir()
	cfg := DefaultConfigWithBasePath(base)

	// nothing expands
	is.Equal(len(cfg.Env.Allow), 0)
	allow, err := cfg.EnvAllowlist()
	is.NoErr(err)
	is.Equal(len(allow.Names()), 0)

	// the file connector is confined to <data dir>/files
	is.Equal(cfg.Connectors.File.AllowedDirs, []string{filepath.Join(base, "files")})

	// custom.javascript: script.path only from <data dir>/scripts, no require()
	fa, err := cfg.javascriptFileAccess()
	is.NoErr(err)
	is.Equal(len(fa.RequireDirs.Dirs()), 0)
	is.Equal(len(fa.ScriptDirs.Dirs()), 1)

	// no private destination is exempt for the built-in HTTP processors
	is.Equal(len(cfg.Processors.Egress.BuiltinAllow), 0)
	_, err = cfg.builtinDialGuard()
	is.NoErr(err)

	is.NoErr(validConfig(t).Validate())
}

// validConfig returns a config that passes Validate, so a test that mutates it
// can attribute a failure to the mutation.
func validConfig(t *testing.T) Config {
	t.Helper()
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "pipelines"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfigWithBasePath(base)
	cfg.DB.Type = DBTypeInMemory
	return cfg
}

func TestConfig_JavaScriptRequireNeedsExplicitDirs(t *testing.T) {
	is := is.New(t)
	d := t.TempDir()
	cfg := DefaultConfigWithBasePath(t.TempDir())
	cfg.Processors.JavaScript.AllowedDirs = []string{d}
	fa, err := cfg.javascriptFileAccess()
	is.NoErr(err)
	is.Equal(len(fa.RequireDirs.Dirs()), 1)
	is.Equal(len(fa.ScriptDirs.Dirs()), 1) // explicit dirs replace the default script dir
	got, _ := filepath.EvalSymlinks(d)
	is.Equal(fa.ScriptDirs.Dirs()[0], got)
}

func TestConfig_Validate_RejectsMalformedHardeningConfig(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"env.allow wildcard":      func(c *Config) { c.Env.Allow = []string{"AWS_*"} },
		"env.allow with equals":   func(c *Config) { c.Env.Allow = []string{"A=B"} },
		"builtin-allow wildcard":  func(c *Config) { c.Processors.Egress.BuiltinAllow = []string{"*.internal"} },
		"builtin-allow http name": func(c *Config) { c.Processors.Egress.BuiltinAllow = []string{"http://ollama.internal:11434"} },
		"file dir with NUL":       func(c *Config) { c.Connectors.File.AllowedDirs = []string{"a\x00b"} },
		"js dir with NUL":         func(c *Config) { c.Processors.JavaScript.AllowedDirs = []string{"a\x00b"} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig(t)
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate accepted a malformed value")
			}
		})
	}
}

func TestSettingsResolver_FileConnectorConfinement(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	files := filepath.Join(root, "files")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{files, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pol, err := pathpolicy.New(FileAllowedDirsKey, []string{files})
	if err != nil {
		t.Fatal(err)
	}
	allow, _ := env.NewAllowlist([]string{"DATA_DIR"})
	r := engineSettingsResolver{env: allow, files: pol}
	t.Setenv("DATA_DIR", files)
	t.Setenv("EVIL_DIR", outside)

	refused := func(t *testing.T, plugin string, settings map[string]string, wantCode conduiterr.Code) {
		t.Helper()
		_, err := r.ResolveSettings(plugin, settings)
		if err == nil {
			t.Fatalf("%s %v accepted", plugin, settings)
		}
		ce, ok := conduiterr.Get(err)
		if !ok || ce.Code != wantCode {
			t.Fatalf("error %v: want code %s", err, wantCode.Reason())
		}
	}

	for _, plugin := range []string{"builtin:file", "file", "builtin:file@v0.10.9", "standalone:file", "builtin:file@latest"} {
		t.Run(plugin, func(t *testing.T) {
			// inside
			out, err := r.ResolveSettings(plugin, map[string]string{"path": filepath.Join(files, "a.out"), "other": "x"})
			if err != nil {
				t.Fatal(err)
			}
			if out["path"] != filepath.Join(files, "a.out") || out["other"] != "x" {
				t.Fatalf("out = %v", out)
			}
			// the allowed env variable expands and the EXPANDED value is checked
			out, err = r.ResolveSettings(plugin, map[string]string{"path": "${DATA_DIR}/b.out"})
			if err != nil || out["path"] != filepath.Join(files, "b.out") {
				t.Fatalf("out = %v, err = %v", out, err)
			}

			refused(t, plugin, map[string]string{"path": filepath.Join(outside, "secret")}, pathpolicy.CodePathNotAllowed)
			refused(t, plugin, map[string]string{"path": "/etc/passwd"}, pathpolicy.CodePathNotAllowed)
			refused(t, plugin, map[string]string{"path": files + "/../outside/secret"}, pathpolicy.CodePathNotAllowed)
			refused(t, plugin, map[string]string{"path": ""}, pathpolicy.CodePathNotAllowed)
			// a variable the operator did not allow cannot be used to build a path
			refused(t, plugin, map[string]string{"path": "${EVIL_DIR}/secret"}, env.CodeEnvVarNotAllowed)
		})
	}

	t.Run("symlink escape", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need privileges on windows")
		}
		if err := os.Symlink(outside, filepath.Join(files, "link")); err != nil {
			t.Fatal(err)
		}
		refused(t, "builtin:file", map[string]string{"path": filepath.Join(files, "link", "secret")}, pathpolicy.CodePathNotAllowed)
	})

	t.Run("other plugins are not path-checked", func(t *testing.T) {
		out, err := r.ResolveSettings("builtin:generator", map[string]string{"path": "/etc/passwd"})
		if err != nil || out["path"] != "/etc/passwd" {
			t.Fatalf("out = %v, err = %v", out, err)
		}
		if _, err := r.ResolveSettings("builtin:postgres", map[string]string{"path": "/x"}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("refusal names the setting", func(t *testing.T) {
		_, err := r.ResolveSettings("builtin:file", map[string]string{"path": "/etc/passwd"})
		ce, _ := conduiterr.Get(err)
		if ce.ConfigPath != "settings.path" || !strings.Contains(ce.Suggestion, FileAllowedDirsKey) {
			t.Fatalf("ConfigPath=%q Suggestion=%q", ce.ConfigPath, ce.Suggestion)
		}
	})
}

// A resolver built without an allowlist or policy (the zero value) denies: it
// is the fail-closed shape a half-wired caller ends up with.
func TestSettingsResolver_ZeroValueDenies(t *testing.T) {
	t.Setenv("X_SECRET", "s")
	r := engineSettingsResolver{}
	if _, err := r.ResolveSettings("builtin:generator", map[string]string{"k": "${X_SECRET}"}); err == nil {
		t.Fatal("env expanded with no allowlist")
	}
	if _, err := r.ResolveSettings("builtin:file", map[string]string{"path": "/tmp/x"}); err == nil {
		t.Fatal("file path accepted with no policy")
	}
}
