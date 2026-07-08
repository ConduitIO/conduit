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
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// ResolveFiles resolves path into the pipeline configuration file(s) it
// refers to: path itself, if it names a file (any extension — a caller that
// points directly at a file is assumed to have chosen it deliberately), or
// every direct child of path (not recursed) whose name ends in .yml or
// .yaml, if path names a directory.
//
// This is the single implementation of "what is a pipeline config path"
// shared by config-file provisioning (pkg/provisioning.Service, which reads
// pipelines at startup) and the offline `conduit pipelines
// validate|lint|dry-run` commands (cmd/conduit/internal/validate), so the
// two surfaces can never drift on the answer.
func ResolveFiles(path string) ([]string, error) {
	if path == "" {
		return nil, cerrors.New("pipeline path cannot be empty")
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, cerrors.Errorf("failed to stat path %q: %w", path, err)
	}
	if !info.IsDir() {
		return []string{path}, nil
	}

	return YAMLFilesInDir(path)
}

// YAMLFilesInDir returns every direct child of dir (not recursed) whose name
// ends in .yml or .yaml (case-insensitive).
func YAMLFilesInDir(dir string) ([]string, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, cerrors.Errorf("could not read directory %q: %w", dir, err)
	}

	var files []string
	for _, e := range dirEntries {
		filePath := filepath.Join(dir, e.Name())
		if IsYAMLFile(filePath) {
			files = append(files, filePath)
		}
	}
	return files, nil
}

// IsYAMLFile reports whether path names a regular file with a .yml or .yaml
// extension (case-insensitive).
func IsYAMLFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yml" || ext == ".yaml"
}
