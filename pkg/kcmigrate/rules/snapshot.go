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
	"regexp"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/yaml/v3"
)

// Snapshot is a committed copy of an upstream key list with defaults
// (rules/upstream/<name>.keys.yaml), the list a rule file was written
// against. Tests use it two ways: every snapshot key must match a rule, and
// every default whose outcome is not plainly translated must have a
// defaults entry, so an absent key cannot hide a difference.
type Snapshot struct {
	Scope    string   `yaml:"scope"`
	Class    string   `yaml:"class"`
	Upstream Upstream `yaml:"upstream"`
	Sources  []string `yaml:"sources"`
	// Check says where the scheduled upstream-drift job reads the live key
	// list (cmd/conduit/internal/kcdrift). Nil for a snapshot no published
	// page lists.
	Check *SnapshotCheck `yaml:"check"`
	Keys  []SnapshotKey  `yaml:"keys"`
}

// SnapshotCheck names the generated upstream pages whose keys and defaults
// the snapshot must equal, and the page keys it leaves out on purpose.
type SnapshotCheck struct {
	// Site is the page format; "kafka" is Apache Kafka's generated config
	// pages, https://kafka.apache.org/<major><minor>/generated/<page>.html.
	Site string `yaml:"site"`
	// Pages are the page names; their keys are merged.
	Pages []string `yaml:"pages"`
	// Ignore lists page keys the snapshot leaves out, each with a reason.
	Ignore []SnapshotIgnore `yaml:"ignore"`
}

// SnapshotIgnore leaves page keys out of the comparison: those matching
// Pattern (an RE2 regex), or the keys of the snapshot named Snapshot (the
// connector-level keys a class page repeats, which the shared snapshot
// tracks).
type SnapshotIgnore struct {
	Pattern  string `yaml:"pattern"`
	Snapshot string `yaml:"snapshot"`
	Reason   string `yaml:"reason"`
}

// SiteKafka is Apache Kafka's generated config pages.
const SiteKafka = "kafka"

// SnapshotKey is one upstream key. Default is nil when the key has no
// upstream default.
type SnapshotKey struct {
	Key     string  `yaml:"key"`
	Default *string `yaml:"default"`
	// Documented is the default the upstream page shows, spelled as the page
	// spells it ("null" for none), when the snapshot records the effective
	// default instead (a connector's header.converter shows null and
	// inherits the worker's SimpleHeaderConverter).
	Documented *string `yaml:"documented"`
	// Undocumented marks a key no checked page lists (a cluster-prefixed
	// client key such as source.cluster.bootstrap.servers).
	Undocumented bool `yaml:"undocumented"`
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
	if c := s.Check; c != nil {
		if c.Site != SiteKafka || len(c.Pages) == 0 {
			return Snapshot{}, cerrors.Errorf("check needs site %q and pages", SiteKafka)
		}
		for _, ig := range c.Ignore {
			if (ig.Pattern == "") == (ig.Snapshot == "") || ig.Reason == "" {
				return Snapshot{}, cerrors.New("an ignore entry needs exactly one of pattern, snapshot, and a reason")
			}
			if ig.Pattern != "" {
				if _, err := regexp.Compile(ig.Pattern); err != nil {
					return Snapshot{}, cerrors.Errorf("ignore pattern %q: %w", ig.Pattern, err)
				}
			}
		}
	}
	return s, nil
}
