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
	"runtime"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// TestStandalonePlugin_EnvAllowlist: a real plugin gets exactly the base set
// plus the operator's names, and the handshake works with that environment.
func TestStandalonePlugin_EnvAllowlist(t *testing.T) {
	dir := buildEnvProbe(t)
	t.Setenv(leakyName, "x")
	t.Setenv("GWVC_ALLOWED_ONE", "1")
	t.Setenv("GWVC_ALLOWED_TWO", "2")
	t.Setenv("GWVC_NOT_LISTED", "3")
	t.Setenv("TZ", "UTC")
	t.Setenv(envLang, "C")
	t.Setenv("LC_TIME", "C")
	t.Setenv("PLUGIN_UNIX_SOCKET_DIR", "/host/should/not/pass")

	policy, err := NewEnvPolicy([]string{"GWVC_ALLOWED_ONE,GWVC_ALLOWED_TWO"})
	if err != nil {
		t.Fatal(err)
	}
	atInit, atDispense := probeEnv(t, dir, WithEnvPolicy(policy))

	for site, names := range map[string][]string{"spec load": atInit, "dispense": atDispense} {
		for _, want := range []string{"GWVC_ALLOWED_ONE", "GWVC_ALLOWED_TWO", "PATH", "TZ", envLang, "LC_TIME", "PLUGIN_MIN_PORT"} {
			if !has(names, want) {
				t.Errorf("%s: missing %s in %v", site, want, names)
			}
		}
		for _, bad := range []string{leakyName, "GWVC_NOT_LISTED", "PLUGIN_UNIX_SOCKET_DIR"} {
			if has(names, bad) {
				t.Errorf("%s: unexpected %s", site, bad)
			}
		}
	}
}

func TestEnvPolicy_Filter(t *testing.T) {
	is := is.New(t)
	p, err := NewEnvPolicy([]string{"EXTRA"})
	is.NoErr(err)

	environ := []string{
		envPath + "=/bin", envHome + "=/h", "TMPDIR=/t", "TEMP=/t", "TMP=/t", "TZ=UTC", "LANG=C", "LANGUAGE=en", "LC_ALL=C", "LC_CTYPE=C",
		"EXTRA=1", "AWS_SECRET_ACCESS_KEY=s", "USER=u", "SHELL=/bin/sh", "path=/lower", "extra=lower",
		"CONDUIT_CONNECTOR_TOKEN=host", "PLUGIN_MIN_PORT=1", "SYSTEMROOT=C:\\\\Windows",
		"EVIL\x1b[2J=x", "=C:=C:\\\\dir", "NOEQUALS",
	}
	passed, stripped, nonStd := p.Filter(environ)

	names := func(kvs []string) []string {
		var out []string
		for _, kv := range kvs {
			n, _, _ := strings.Cut(kv, "=")
			out = append(out, n)
		}
		return out
	}
	got := names(passed)
	for _, want := range []string{"PATH", "HOME", "TMPDIR", "TEMP", "TMP", "TZ", envLang, "LANGUAGE", "LC_ALL", "LC_CTYPE", "EXTRA"} {
		is.True(has(got, want))
	}
	if runtime.GOOS != goosWindows {
		for _, bad := range []string{"AWS_SECRET_ACCESS_KEY", "USER", "SHELL", "path", "extra", "CONDUIT_CONNECTOR_TOKEN", "PLUGIN_MIN_PORT", "SYSTEMROOT"} {
			is.True(!has(got, bad)) // case-sensitive on Unix; reserved never; SYSTEMROOT only on Windows
			is.True(has(stripped, bad))
		}
	}
	is.Equal(nonStd, 1) // the escape-sequence name is counted, never listed
	for _, s := range stripped {
		is.True(!strings.Contains(s, "\x1b"))
	}
}

func TestEnvPolicy_Windows(t *testing.T) {
	is := is.New(t)
	p, err := NewEnvPolicy([]string{"Extra"})
	is.NoErr(err)
	p.goos = goosWindows
	p.allow = map[string]struct{}{"EXTRA": {}}
	passed, stripped, _ := p.Filter([]string{"Path=C:\\\\", "SystemRoot=C:\\\\Windows", "extra=1", "ComSpec=x", "AWS_KEY=s"})
	is.Equal(len(passed), 3)
	is.Equal(stripped, []string{"AWS_KEY", "ComSpec"})
}

func TestNewEnvPolicy_Validation(t *testing.T) {
	is := is.New(t)
	for _, bad := range []string{"AWS_*", "A=B", "1ABC", "A-B", "${A}", "CONDUIT_CONNECTOR_TOKEN", "conduit_connector_id", "PLUGIN_MIN_PORT"} {
		_, err := NewEnvPolicy([]string{bad})
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.ConfigPath, EnvAllowConfigPath)
		is.True(ce.Suggestion != "")
	}
	p, err := NewEnvPolicy([]string{"A, B", "C"})
	is.NoErr(err)
	is.Equal(len(p.allow), 3)
}
