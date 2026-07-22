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

package registry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// StandaloneArtifactKind is the only artifact kind this install pipeline
// knows how to install. A future kind (e.g. a WASM processor-shaped
// artifact) is skipped, not treated as an error, by SelectArtifact — an
// unrecognized kind refuses gracefully via "no matching artifact", never a
// crash.
const StandaloneArtifactKind = "standalone"

// SelectArtifact finds the artifact in v matching the exact (goos, goarch)
// pair among StandaloneArtifactKind entries. No match refuses with
// CodeNoPlatformArtifact, listing the (os, arch) pairs that DO exist for
// this version so the operator/agent knows what is actually available
// rather than just "not found".
func SelectArtifact(connName string, v index.ConnectorVersion, goos, goarch string) (*index.Artifact, error) {
	for i := range v.Artifacts {
		a := v.Artifacts[i]
		if a.Kind != StandaloneArtifactKind {
			continue
		}
		if a.OS == goos && a.Arch == goarch {
			return &a, nil
		}
	}

	var available []string
	for _, a := range v.Artifacts {
		if a.Kind == StandaloneArtifactKind {
			available = append(available, a.OS+"/"+a.Arch)
		}
	}
	sort.Strings(available)

	err := conduiterr.New(CodeNoPlatformArtifact, fmt.Sprintf(
		"connector %q version %s has no standalone artifact for %s/%s", connName, v.Version, goos, goarch))
	if len(available) > 0 {
		err.Suggestion = "available platforms for this version: " + strings.Join(available, ", ")
	} else {
		err.Suggestion = "this version has no standalone artifacts for any platform"
	}
	return nil, err
}
