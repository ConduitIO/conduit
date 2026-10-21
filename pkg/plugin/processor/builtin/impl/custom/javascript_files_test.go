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

package custom

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/pathpolicy"
	"github.com/matryer/is"
)

const jsSecretMarker = "JS-SECRET-MARKER-9f2c"

type jsFiles struct {
	root    string
	allowed string
	outside string
}

func newJSFiles(t *testing.T) jsFiles {
	t.Helper()
	root, err := os.MkdirTemp("", "jsfiles")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, _ = filepath.EvalSymlinks(root)
	f := jsFiles{root: root, allowed: filepath.Join(root, "allowed"), outside: filepath.Join(root, "outside")}
	for _, d := range []string{f.allowed, f.outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A "secret" that is valid JS so that, if it were loaded, it would run and
	// the marker would show up in the module's exports.
	write(t, filepath.Join(f.outside, "secret.js"), "module.exports = '"+jsSecretMarker+"';")
	write(t, filepath.Join(f.outside, "script.js"), "function process(r) { return r; }")
	write(t, filepath.Join(f.allowed, "lib.js"), "module.exports = 'inside-lib';")
	write(t, filepath.Join(f.allowed, "script.js"), "function process(r) { return r; }")
	return f
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func policy(t *testing.T, dirs ...string) *pathpolicy.Policy {
	t.Helper()
	p, err := pathpolicy.New("processors.javascript.allowed-dirs", dirs)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func openWith(t *testing.T, fa FileAccess, cfg config.Config) error {
	t.Helper()
	p := NewJavascriptProcessor(log.Nop())
	p.SetFileAccess(fa)
	ctx := context.Background()
	if err := p.Configure(ctx, cfg); err != nil {
		return err
	}
	return p.Open(ctx)
}

func requireScript(modulePath string) config.Config {
	return config.Config{"script": `
var m = require(` + "`" + modulePath + "`" + `);
function process(r) { r.Metadata["m"] = String(m); return r; }`}
}

func TestJS_ScriptPath_DefaultDeny(t *testing.T) {
	is := is.New(t)
	f := newJSFiles(t)
	err := openWith(t, FileAccess{}, config.Config{"script": "", "script.path": filepath.Join(f.allowed, "script.js")})
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, pathpolicy.CodePathNotAllowed)
	is.Equal(ce.ConfigPath, "settings.script.path")
}

func TestJS_ScriptPath_Confined(t *testing.T) {
	f := newJSFiles(t)
	fa := FileAccess{ScriptDirs: policy(t, f.allowed)}

	if err := openWith(t, fa, config.Config{"script": "", "script.path": filepath.Join(f.allowed, "script.js")}); err != nil {
		t.Fatalf("script inside the allowed dir refused: %v", err)
	}
	for name, path := range map[string]string{
		"outside":         filepath.Join(f.outside, "script.js"),
		"dotdot":          filepath.Join(f.allowed, "..", "outside", "script.js"),
		"etc passwd":      "/etc/passwd",
		"relative dotdot": "../outside/script.js",
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			t.Chdir(f.allowed)
			err := openWith(t, fa, config.Config{"script": "", "script.path": path})
			is.True(err != nil)
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, pathpolicy.CodePathNotAllowed)
		})
	}

	t.Run("symlink escape", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need privileges on windows")
		}
		is := is.New(t)
		link := filepath.Join(f.allowed, "evil.js")
		is.NoErr(os.Symlink(filepath.Join(f.outside, "script.js"), link))
		err := openWith(t, fa, config.Config{"script": "", "script.path": link})
		is.True(err != nil)
		_, ok := conduiterr.Get(err)
		is.True(ok)
	})
}

func TestJS_RequireIsNotDefinedByDefault(t *testing.T) {
	is := is.New(t)
	f := newJSFiles(t)
	// no RequireDirs: require is a ReferenceError, whatever the path
	err := openWith(t, FileAccess{ScriptDirs: policy(t, f.allowed)}, requireScript(filepath.Join(f.allowed, "lib.js")))
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "require"))

	err = openWith(t, FileAccess{}, requireScript(filepath.Join(f.outside, "secret.js")))
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "require"))
}

func TestJS_RequireConfinedToAllowedDirs(t *testing.T) {
	f := newJSFiles(t)
	fa := FileAccess{RequireDirs: policy(t, f.allowed)}

	t.Run("inside works", func(t *testing.T) {
		if err := openWith(t, fa, requireScript(filepath.Join(f.allowed, "lib.js"))); err != nil {
			t.Fatalf("require inside the allowed dir failed: %v", err)
		}
	})

	for name, path := range map[string]string{
		"absolute outside":           filepath.Join(f.outside, "secret.js"),
		"absolute without extension": filepath.Join(f.outside, "secret"),
		"dotdot out of allowed":      filepath.Join(f.allowed, "..", "outside", "secret.js"),
		"absolute etc passwd":        "/etc/passwd",
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			err := openWith(t, fa, requireScript(path))
			is.True(err != nil)
			is.True(!strings.Contains(err.Error(), jsSecretMarker))
		})
	}

	t.Run("relative dotdot", func(t *testing.T) {
		is := is.New(t)
		t.Chdir(f.allowed)
		err := openWith(t, fa, requireScript("../outside/secret.js"))
		is.True(err != nil)
	})

	t.Run("symlink escape", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need privileges on windows")
		}
		is := is.New(t)
		is.NoErr(os.Symlink(filepath.Join(f.outside, "secret.js"), filepath.Join(f.allowed, "evil.js")))
		is.NoErr(os.Symlink(f.outside, filepath.Join(f.allowed, "evildir")))
		for _, p := range []string{filepath.Join(f.allowed, "evil.js"), filepath.Join(f.allowed, "evildir", "secret.js")} {
			err := openWith(t, fa, requireScript(p))
			is.True(err != nil)
		}
	})

	t.Run("module outside is never executed", func(t *testing.T) {
		is := is.New(t)
		// If the loader ran the outside module, the script below would copy its
		// export into the error; the loader must refuse before that.
		err := openWith(t, fa, config.Config{"script": `
var m = require("` + filepath.Join(f.outside, "secret.js") + `");
throw new Error("LEAK:" + m);
function process(r) { return r; }`})
		is.True(err != nil)
		is.True(!strings.Contains(err.Error(), "LEAK:"+jsSecretMarker))
	})
}
