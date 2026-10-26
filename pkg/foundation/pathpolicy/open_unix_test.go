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

//go:build unix

package pathpolicy

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenFile_ReadWriteInside(t *testing.T) {
	f := newFixture(t)
	file, err := f.policy.OpenFile("settings.path", filepath.Join(f.allowed, "ok"), os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	b, _ := io.ReadAll(file)
	if string(b) != "ok" {
		t.Fatalf("read %q", b)
	}

	// create below a subdirectory, with the requested (umask-limited) mode
	out := filepath.Join(f.allowed, "sub", "new.out")
	w, err := f.policy.OpenFile("settings.path", out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString("a")
	w.Close()
	w, err = f.policy.OpenFile("settings.path", out, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString("b")
	w.Close()
	if got, _ := os.ReadFile(out); string(got) != "ab" {
		t.Fatalf("content %q", got)
	}
	if fi, _ := os.Stat(out); fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestOpenFile_RefusesOutside(t *testing.T) {
	f := newFixture(t)
	if _, err := f.policy.OpenFile("settings.path", filepath.Join(f.outside, "secret"), os.O_RDONLY, 0); err == nil {
		t.Fatal("opened a file outside the allowed directory")
	}
}

// TestOpenBeneath_SwapAfterCheck simulates the attack the open-time check
// exists for: the path passes Check, then a path component is replaced by a
// symlink to the outside before the open happens.
func TestOpenBeneath_SwapAfterCheck(t *testing.T) {
	f := newFixture(t)

	// final component swapped for a symlink to an outside file
	target := filepath.Join(f.allowed, "sub", "data")
	if err := os.WriteFile(target, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, root, err := f.policy.check("settings.path", target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.outside, "secret"), target); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(root, resolved)
	if file, err := openBeneath(root, resolved, rel, os.O_RDONLY, 0); err == nil {
		b, _ := io.ReadAll(file)
		file.Close()
		t.Fatalf("followed a swapped final symlink and read %q", b)
	}

	// intermediate directory swapped for a symlink to an outside directory
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.outside, "data"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, root, err = f.policy.check("settings.path", target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.allowed, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.outside, filepath.Join(f.allowed, "sub")); err != nil {
		t.Fatal(err)
	}
	rel, _ = filepath.Rel(root, resolved)
	if file, err := openBeneath(root, resolved, rel, os.O_RDONLY, 0); err == nil {
		b, _ := io.ReadAll(file)
		file.Close()
		t.Fatalf("followed a swapped directory symlink and read %q", b)
	}

	// and creating through the swapped directory must not create outside
	rel2 := filepath.Join("sub", "created-through-swap")
	if file, err := openBeneath(root, filepath.Join(root, rel2), rel2, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		file.Close()
		t.Fatal("created a file through a swapped directory symlink")
	}
	if _, err := os.Stat(filepath.Join(f.outside, "created-through-swap")); err == nil {
		t.Fatal("file was created outside")
	}
}

// The portable walk is the fallback where openat2 is missing; exercise it
// directly on every platform.
func TestOpenBeneath_PortableWalkRefusesSymlinks(t *testing.T) {
	f := newFixture(t)
	if err := os.Symlink(f.outside, filepath.Join(f.allowed, "dir-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.outside, "secret"), filepath.Join(f.allowed, "file-link")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"dir-link/secret", "file-link"} {
		if file, err := openBeneath(f.allowed, filepath.Join(f.allowed, rel), rel, os.O_RDONLY, 0); err == nil {
			file.Close()
			t.Errorf("%s: opened through a symlink", rel)
		}
	}
	for _, rel := range []string{"../outside/secret", "sub/../../outside/secret", "./ok", "", "a//b"} {
		if file, err := openBeneath(f.allowed, filepath.Join(f.allowed, rel), rel, os.O_RDONLY, 0); err == nil {
			file.Close()
			t.Errorf("%q: accepted a relative path with climbing/empty components", rel)
		}
	}
}
