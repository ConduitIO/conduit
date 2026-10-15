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

package rules

import (
	"bytes"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/yaml/v3"
)

// Snapshot is a committed copy of an upstream key list with defaults
// (rules/upstream/<name>.keys.yaml), the list a rule file was written
// against. Tests use it two ways: every snapshot key must match a rule, and
// every default whose outcome is not plainly translated must have a
// defaults entry, so an absent key cannot hide a difference.
type Snapshot struct {
	Scope    string        `yaml:"scope"`
	Class    string        `yaml:"class"`
	Upstream Upstream      `yaml:"upstream"`
	Sources  []string      `yaml:"sources"`
	Keys     []SnapshotKey `yaml:"keys"`
}

// SnapshotKey is one upstream key. Default is nil when the key has no
// upstream default.
type SnapshotKey struct {
	Key     string  `yaml:"key"`
	Default *string `yaml:"default"`
}

// ParseSnapshot decodes a snapshot strictly.
func ParseSnapshot(b []byte) (Snapshot, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var s Snapshot
	if err := dec.Decode(&s); err != nil {
		return Snapshot{}, cerrors.Errorf("decode snapshot: %w", err)
	}
	if s.Upstream.Name == "" || s.Upstream.Version == "" || len(s.Keys) == 0 || len(s.Sources) == 0 {
		return Snapshot{}, cerrors.New("a snapshot needs upstream, sources and keys")
	}
	return s, nil
}
