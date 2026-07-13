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

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// findModuleRoot walks upward from dir looking for a go.mod, returning the
// directory that contains it. `go generate` runs a directive with the
// package directory as its working directory (here,
// cmd/conduit/internal/llmsgen); `go run ./cmd/conduit/internal/llmsgen`
// from the repo root does not change that — os.Getwd reflects the caller's
// shell, not the target package. Walking up guarantees the generator finds
// the same go.mod, and therefore writes llms.txt/llms-full.txt to the same
// repo root, regardless of which of those two ways it was invoked.
func findModuleRoot(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above %s", dir)
		}
		dir = parent
	}
}
