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
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// NormalizeVersion parses a version string as semver, tolerating an
// optional leading "v" (plan-v2 §5): @version matching is semver equality,
// never raw string equality. The index stores bare semver ("0.14.0"); user
// input, manifest entries, and CLI flags may carry either form ("v0.14.0"
// or "0.14.0").
//
// Every version comparison in this codebase — Resolve's exact-@version
// lookup (PR-1), manifest key construction (ManifestKey), minConduitVersion
// /minProtocolVersion compatibility checks, audit's installed-version-vs-
// index lookup (PR-4) — goes through NormalizeVersion and
// semver.Version.Equal/.Compare, never a bare string comparison. This
// directly prevents the bug plan-v2-requirements.md calls out: a
// "@v0.14.0" install request must resolve against an index entry stored as
// "0.14.0", not spuriously 404.
func NormalizeVersion(s string) (*semver.Version, error) {
	v, err := semver.NewVersion(strings.TrimPrefix(s, "v"))
	if err != nil {
		return nil, cerrors.Errorf("invalid version %q: %w", s, err)
	}
	return v, nil
}
