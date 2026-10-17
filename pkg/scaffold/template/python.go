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

package template

import (
	"bytes"
	"embed"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

//go:embed all:connector_python
var connectorPythonFS embed.FS

// ConnectorPythonRef records the source of the Python connector snapshot.
// Unlike the Go templates — vendored from upstream template repos at a pinned
// git SHA — the Python connector template is authored in this repository and
// mirrors the SDK's own worked example; there is no upstream repo to pin.
const ConnectorPythonRef = "in-repo"

// ConnectorPythonSDKVersion is the `conduitio` distribution version the Python
// connector template pins in its pyproject.toml. It is reported in Result.
// SDKVersion for --lang python. Bump in lockstep with the pyproject.toml pin.
const ConnectorPythonSDKVersion = "0.1.0.dev0"

// pythonNamePlaceholder is the token the Python template uses for the
// connector's resource name, and what RewritePython replaces it with. It
// matches the Go templates' namePlaceholder so the two languages share one
// token shape.
const pythonNamePlaceholder = "connectorname"

// ExtractPython writes the embedded Python connector snapshot into destDir,
// which must already exist. Unlike Extract (the Go templates), the Python
// snapshot has no .tmpl-renamed files (there is no nested go.mod boundary to
// protect against) and no executable files, so this is a plain file copy with
// the same staging/atomicity discipline Extract's callers already provide.
func ExtractPython(destDir string) error {
	src, err := fs.Sub(connectorPythonFS, "connector_python")
	if err != nil {
		return cerrors.Errorf("template: sub python snapshot: %w", err)
	}

	return fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return cerrors.Errorf("template: walking python snapshot at %q: %w", path, err)
		}
		if path == "." {
			return nil
		}

		target := filepath.Join(destDir, filepath.FromSlash(path))
		if d.IsDir() {
			if mkErr := os.MkdirAll(target, 0o755); mkErr != nil {
				return cerrors.Errorf("template: creating directory %q: %w", target, mkErr)
			}
			return nil
		}

		data, readErr := fs.ReadFile(src, path)
		if readErr != nil {
			return cerrors.Errorf("template: reading %q from python snapshot: %w", path, readErr)
		}
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
			return cerrors.Errorf("template: creating directory %q: %w", filepath.Dir(target), mkErr)
		}
		if writeErr := os.WriteFile(target, data, 0o644); writeErr != nil {
			return cerrors.Errorf("template: writing %q: %w", target, writeErr)
		}
		return nil
	})
}

// RewritePython replaces the connector name placeholder in every file under
// dir. The Python template has no module path and no CODEOWNERS/README
// promotion (both are Go-template setup.sh behaviors), so this is a single
// token substitution over the extracted tree.
func RewritePython(dir, name string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return cerrors.Errorf("template: opening %q as a root: %w", dir, err)
	}
	defer root.Close()

	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return cerrors.Errorf("template: walking %q: %w", path, err)
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return cerrors.Errorf("template: computing relative path for %q: %w", path, err)
		}

		data, err := root.ReadFile(rel)
		if err != nil {
			return cerrors.Errorf("template: reading %q: %w", rel, err)
		}

		rewritten := bytes.ReplaceAll(data, []byte(pythonNamePlaceholder), []byte(name))
		if bytes.Equal(rewritten, data) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return cerrors.Errorf("template: stat %q: %w", path, err)
		}
		if err := root.WriteFile(rel, rewritten, info.Mode()); err != nil {
			return cerrors.Errorf("template: writing %q: %w", rel, err)
		}
		return nil
	})
}
