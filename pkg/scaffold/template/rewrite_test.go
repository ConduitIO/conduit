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

package template_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/conduitio/conduit/pkg/scaffold/template"
	"github.com/stretchr/testify/require"
)

// TestRewrite_Golden extracts and rewrites each template kind against a
// fixed module/name and asserts the resulting tree is byte-for-byte
// identical (including which files are executable) to the checked-in golden
// fixture under testdata/golden. This is the "setup.sh -> Go rewrite is
// golden-file tested (rewritten tree byte-stable)" acceptance criterion —
// any unintended drift in Extract or Rewrite's output shows up here as a
// file-content or file-list diff, not as a downstream `go build` failure
// with no clue which step caused it.
//
// To regenerate the golden fixture after an intentional change (e.g.
// resyncing the embedded snapshot to a newer upstream ref), see this
// file's TestMain-adjacent regenerate helper is deliberately not wired to a
// flag: run the Extract+Rewrite pair by hand (as this test does) against
// testdata/golden/<kind> and inspect the diff before committing it — a
// -update flag that silently overwrites the golden file would defeat the
// point of a golden test that's supposed to catch accidental drift.
func TestRewrite_Golden(t *testing.T) {
	tests := []struct {
		kind   template.Kind
		module string
		name   string
	}{
		{template.KindConnector, "github.com/conduitio/conduit-connector-widget", "widget"},
		{template.KindProcessor, "github.com/conduitio/conduit-processor-widget", "widget"},
	}

	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			got := t.TempDir()
			require.NoError(t, template.Extract(tt.kind, got))
			require.NoError(t, template.Rewrite(got, tt.module, tt.name, tt.kind))

			want := filepath.Join("testdata", "golden", string(tt.kind))
			requireIdenticalTrees(t, want, got)
		})
	}
}

// requireIdenticalTrees asserts want and got contain the same relative file
// paths, with identical content and identical "is this executable" bits
// (the specific permission value isn't compared, since umask/OS can cause
// harmless 0644-vs-0640-style variance — only the executable bit, which
// Extract sets deliberately per template.executableFiles, is asserted).
func requireIdenticalTrees(t *testing.T, want, got string) {
	t.Helper()

	wantFiles := listFiles(t, want)
	gotFiles := listFiles(t, got)
	require.Equal(t, wantFiles, gotFiles, "file list differs between golden fixture and rewritten output")

	for _, rel := range wantFiles {
		wantPath := filepath.Join(want, rel)
		gotPath := filepath.Join(got, rel)

		wantData, err := os.ReadFile(wantPath)
		require.NoError(t, err)
		gotData, err := os.ReadFile(gotPath)
		require.NoError(t, err)
		require.Equal(t, string(wantData), string(gotData), "content differs for %s", rel)

		wantInfo, err := os.Stat(wantPath)
		require.NoError(t, err)
		gotInfo, err := os.Stat(gotPath)
		require.NoError(t, err)
		require.Equal(t, isExecutable(wantInfo.Mode()), isExecutable(gotInfo.Mode()), "executable bit differs for %s", rel)
	}
}

func isExecutable(mode os.FileMode) bool {
	return mode&0o111 != 0
}

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	require.NoError(t, err)
	sort.Strings(files)
	return files
}
