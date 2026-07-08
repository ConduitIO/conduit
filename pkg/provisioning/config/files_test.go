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

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matryer/is"
)

func TestResolveFiles_EmptyPath(t *testing.T) {
	is := is.New(t)
	_, err := ResolveFiles("")
	is.True(err != nil)
	is.True(err.Error() == "pipeline path cannot be empty")
}

func TestResolveFiles_NonexistentPath(t *testing.T) {
	is := is.New(t)
	_, err := ResolveFiles(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	is.True(err != nil)
}

func TestResolveFiles_SingleFile_AnyExtension(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	f := filepath.Join(dir, "pipeline.txt") // deliberately not .yml/.yaml
	is.NoErr(os.WriteFile(f, []byte("version: 2.2\n"), 0o600))

	got, err := ResolveFiles(f)
	is.NoErr(err)
	is.Equal(got, []string{f})
}

func TestResolveFiles_Directory_OnlyYAMLNotRecursed(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	yml := filepath.Join(dir, "a.yml")
	yaml := filepath.Join(dir, "b.yaml")
	txt := filepath.Join(dir, "c.txt")
	is.NoErr(os.WriteFile(yml, []byte(""), 0o600))
	is.NoErr(os.WriteFile(yaml, []byte(""), 0o600))
	is.NoErr(os.WriteFile(txt, []byte(""), 0o600))

	nested := filepath.Join(dir, "nested")
	is.NoErr(os.Mkdir(nested, 0o750))
	is.NoErr(os.WriteFile(filepath.Join(nested, "d.yaml"), []byte(""), 0o600))

	got, err := ResolveFiles(dir)
	is.NoErr(err)
	is.Equal(len(got), 2)
	for _, f := range got {
		is.True(f == yml || f == yaml)
	}
}

func TestResolveFiles_EmptyDirectory(t *testing.T) {
	is := is.New(t)
	got, err := ResolveFiles(t.TempDir())
	is.NoErr(err)
	is.Equal(len(got), 0)
}

func TestIsYAMLFile(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	yml := filepath.Join(dir, "a.YML") // case-insensitive
	is.NoErr(os.WriteFile(yml, []byte(""), 0o600))
	is.True(IsYAMLFile(yml))

	txt := filepath.Join(dir, "a.txt")
	is.NoErr(os.WriteFile(txt, []byte(""), 0o600))
	is.True(!IsYAMLFile(txt))

	is.True(!IsYAMLFile(filepath.Join(dir, "does-not-exist.yaml")))
	is.True(!IsYAMLFile(dir)) // a directory is not a regular file
}
