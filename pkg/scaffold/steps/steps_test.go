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

package steps_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/pkg/scaffold/steps"
	"github.com/conduitio/conduit/pkg/scaffold/template"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallTool_UnknownKind(t *testing.T) {
	err := steps.InstallTool(context.Background(), t.TempDir(), template.Kind("bogus"))
	require.Error(t, err)
}

func TestBuild_Fails_OnBrokenSource(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/broken\n\ngo 1.25.0\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { this is not go }\n"), 0o644))

	err := steps.Build(context.Background(), dir)
	assert.Error(t, err)
}

func TestBuild_Succeeds(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/ok\n\ngo 1.25.0\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))

	assert.NoError(t, steps.Build(context.Background(), dir))
}

func TestGitInit_CreatesRepoAndCommit(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644))

	require.NoError(t, steps.GitInit(context.Background(), dir, "chore: initial scaffold"))
	assert.DirExists(t, filepath.Join(dir, ".git"))
}
