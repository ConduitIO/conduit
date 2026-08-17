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

package generate

import (
	"path"
	"path/filepath"
	"strings"
)

// DefaultOutName is used when the candidate carries no usable pipeline id.
const DefaultOutName = "pipeline.yaml"

// modelDerivedName turns a model-chosen pipeline id into a filename that
// cannot escape the working directory.
//
// This is a security boundary, not a formatting nicety, and it is independent
// of the TTY/confirmation boundary: writing the candidate to disk needs no
// confirmation at all, so a model-influenced string must never be
// interpretable as a path. Design §5 states the rule; this is where it is
// enforced.
//
// Everything up to the last separator is discarded (filepath.Base), and the
// result is rejected if it is still relative-traversal or empty — belt and
// braces, since Base already strips "..\..\etc" down to "etc" on the
// platform's own separator but a caller reading this should not have to
// reason about that to be sure.
func modelDerivedName(pipelineID string) string {
	name := strings.TrimSpace(pipelineID)
	if name == "" {
		return DefaultOutName
	}

	// Normalize both separators before taking the base: a model can emit a
	// Windows-style path on Linux, where filepath.Base would keep the
	// backslashes as part of the "name".
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)

	switch name {
	case "", ".", "..", string(filepath.Separator):
		return DefaultOutName
	}
	if strings.Contains(name, "..") {
		return DefaultOutName
	}

	if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
		name += ".yaml"
	}
	return name
}

// resolveOutPath decides where a generated pipeline is written.
//
// An explicit --out is taken as given: the USER chose it, not the model, so it
// is treated like any other CLI file argument (resolved relative to the
// working directory, allowed to point wherever the user can write). A path
// derived from the model's pipeline id is confined to a bare basename in the
// working directory. That asymmetry is deliberate and is the whole point of
// the split.
func resolveOutPath(outFlag, pipelineID string) string {
	if strings.TrimSpace(outFlag) != "" {
		return outFlag
	}
	return modelDerivedName(pipelineID)
}
