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

package pathpolicy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

type fixture struct {
	root    string // resolved temp root
	allowed string
	outside string
	policy  *Policy
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{root: root, allowed: filepath.Join(root, "allowed"), outside: filepath.Join(root, "outside")}
	for _, d := range []string{f.allowed, f.outside, filepath.Join(f.allowed, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.outside, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.allowed, "ok"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.policy, err = New("connectors.file.allowed-dirs", []string{f.allowed})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func mustAllow(t *testing.T, p *Policy, path string) string {
	t.Helper()
	got, err := p.Check("settings.path", path)
	if err != nil {
		t.Fatalf("Check(%q) = %v, want allowed", path, err)
	}
	return got
}

func mustRefuse(t *testing.T, p *Policy, path string) {
	t.Helper()
	got, err := p.Check("settings.path", path)
	if err == nil {
		t.Fatalf("Check(%q) = %q, want refusal", path, got)
	}
	ce, ok := conduiterr.Get(err)
	if !ok || ce.Code != CodePathNotAllowed {
		t.Fatalf("error %v is not coded %s", err, CodePathNotAllowed.Reason())
	}
	if ce.ConfigPath != "settings.path" {
		t.Fatalf("ConfigPath = %q", ce.ConfigPath)
	}
	if p != nil && !strings.Contains(ce.Suggestion, p.operatorKey) {
		t.Fatalf("suggestion %q does not name the operator key", ce.Suggestion)
	}
	if got != "" {
		t.Fatalf("returned path %q on refusal", got)
	}
}

func TestCheck_InsideAllowed(t *testing.T) {
	f := newFixture(t)
	mustAllow(t, f.policy, filepath.Join(f.allowed, "ok"))
	mustAllow(t, f.policy, filepath.Join(f.allowed, "does-not-exist-yet.out")) // destination to be created
	mustAllow(t, f.policy, filepath.Join(f.allowed, "sub", "new", "deeper.out"))
	// "../" that stays inside is fine once cleaned
	mustAllow(t, f.policy, filepath.Join(f.allowed, "sub", "..", "ok"))
}

func TestCheck_DotDotEscapes(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{
		f.allowed + "/../outside/secret",
		f.allowed + "/sub/../../outside/secret",
		f.allowed + "/../allowed-sibling",
		f.allowed + "/./../outside",
		f.allowed + "/sub/../../../../../../../../etc/passwd",
		filepath.Join(f.outside, "secret"),
		"/etc/passwd",
		"/",
	} {
		mustRefuse(t, f.policy, p)
	}
	// the allowed dir's name as a prefix of a sibling is not containment
	sibling := f.allowed + "-evil"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	mustRefuse(t, f.policy, filepath.Join(sibling, "x"))
	// the directory itself is not a file inside it
	mustRefuse(t, f.policy, f.allowed)
}

func TestCheck_SymlinkEscapes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	f := newFixture(t)

	// file symlink to a file outside
	if err := os.Symlink(filepath.Join(f.outside, "secret"), filepath.Join(f.allowed, "link-file")); err != nil {
		t.Fatal(err)
	}
	mustRefuse(t, f.policy, filepath.Join(f.allowed, "link-file"))

	// directory symlink to a directory outside, then a path through it
	if err := os.Symlink(f.outside, filepath.Join(f.allowed, "link-dir")); err != nil {
		t.Fatal(err)
	}
	mustRefuse(t, f.policy, filepath.Join(f.allowed, "link-dir", "secret"))
	mustRefuse(t, f.policy, filepath.Join(f.allowed, "link-dir", "new-file-to-create"))

	// relative symlink that climbs out
	if err := os.Symlink("../../outside/secret", filepath.Join(f.allowed, "sub", "rel-link")); err != nil {
		t.Fatal(err)
	}
	mustRefuse(t, f.policy, filepath.Join(f.allowed, "sub", "rel-link"))

	// chain of links that ends outside
	if err := os.Symlink(filepath.Join(f.allowed, "link-file"), filepath.Join(f.allowed, "chain1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.allowed, "chain1"), filepath.Join(f.allowed, "chain2")); err != nil {
		t.Fatal(err)
	}
	mustRefuse(t, f.policy, filepath.Join(f.allowed, "chain2"))

	// dangling symlink: creating the "file" would create its outside target
	if err := os.Symlink(filepath.Join(f.outside, "to-be-created"), filepath.Join(f.allowed, "dangling")); err != nil {
		t.Fatal(err)
	}
	mustRefuse(t, f.policy, filepath.Join(f.allowed, "dangling"))

	// symlink loop is refused, not hung on
	if err := os.Symlink(filepath.Join(f.allowed, "loop-b"), filepath.Join(f.allowed, "loop-a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.allowed, "loop-a"), filepath.Join(f.allowed, "loop-b")); err != nil {
		t.Fatal(err)
	}
	mustRefuse(t, f.policy, filepath.Join(f.allowed, "loop-a"))

	// a symlink that stays inside is fine, and the resolved path is returned
	if err := os.Symlink(filepath.Join(f.allowed, "ok"), filepath.Join(f.allowed, "inner-link")); err != nil {
		t.Fatal(err)
	}
	if got := mustAllow(t, f.policy, filepath.Join(f.allowed, "inner-link")); got != filepath.Join(f.allowed, "ok") {
		t.Fatalf("resolved = %q", got)
	}
}

func TestCheck_AllowedDirIsItselfASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	f := newFixture(t)
	alias := filepath.Join(f.root, "alias")
	if err := os.Symlink(f.allowed, alias); err != nil {
		t.Fatal(err)
	}
	p, err := New("k", []string{alias})
	if err != nil {
		t.Fatal(err)
	}
	mustAllow(t, p, filepath.Join(alias, "ok"))
	mustAllow(t, p, filepath.Join(f.allowed, "ok"))
	mustRefuse(t, p, filepath.Join(f.outside, "secret"))
}

func TestCheck_DefaultsDeny(t *testing.T) {
	f := newFixture(t)
	var nilPolicy *Policy
	mustRefuse(t, nilPolicy, filepath.Join(f.allowed, "ok"))
	empty, err := New("k", nil)
	if err != nil {
		t.Fatal(err)
	}
	mustRefuse(t, empty, filepath.Join(f.allowed, "ok"))
	mustRefuse(t, f.policy, "")
	mustRefuse(t, f.policy, filepath.Join(f.allowed, "ok")+"\x00.txt")
}

func TestCheck_RelativePathsResolveAgainstCwd(t *testing.T) {
	f := newFixture(t)
	t.Chdir(f.outside)
	mustRefuse(t, f.policy, "secret")
	mustRefuse(t, f.policy, "../allowed/../outside/secret")
	t.Chdir(f.allowed)
	mustAllow(t, f.policy, "ok")
	mustAllow(t, f.policy, "./new.out")
}

func TestRefusalDoesNotDescribeTheFileSystem(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	f := newFixture(t)
	link := filepath.Join(f.allowed, "l")
	if err := os.Symlink(filepath.Join(f.outside, "secret"), link); err != nil {
		t.Fatal(err)
	}
	_, err := f.policy.Check("settings.path", link)
	if err == nil {
		t.Fatal("want refusal")
	}
	if strings.Contains(err.Error(), "outside/secret") {
		t.Fatalf("message leaks the symlink target: %v", err)
	}
}

func TestNew_Entries(t *testing.T) {
	f := newFixture(t)
	p, err := New("k", []string{f.allowed + "," + f.outside})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Dirs()) != 2 {
		t.Fatalf("dirs = %v", p.Dirs())
	}
	if _, err := New("k", []string{"a\x00b"}); err == nil {
		t.Fatal("NUL accepted")
	}
	// a directory that does not exist yet is accepted
	p, err = New("k", []string{filepath.Join(f.root, "not", "yet")})
	if err != nil {
		t.Fatal(err)
	}
	mustAllow(t, p, filepath.Join(f.root, "not", "yet", "file"))
}
