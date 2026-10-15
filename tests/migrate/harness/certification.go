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

package harness

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/yaml/v3"
)

// Certification tiers a certification.yaml entry may declare. "certified"
// matches the registry index tier of the same name (pkg/registry/index,
// TierCertified). "pending" is the harness's own value for a plugin a lane
// has not certified yet: the entry exists, so the per-PR gate (rule 4) holds,
// and the release gate fails on it.
const (
	TierCertified = "certified"
	TierPending   = "pending"
)

// CertificationEntry is one element of certification.yaml.
type CertificationEntry struct {
	Plugin       string                `yaml:"plugin"`
	Version      string                `yaml:"version"`
	Tier         string                `yaml:"tier"`
	Lane         int                   `yaml:"lane"`
	Registry     string                `yaml:"registry"`
	Capabilities map[string]Capability `yaml:"capabilities"`
}

// Capability is a behavior contract claimed for a plugin, with its proving
// test (design, "requires gate").
type Capability struct {
	Test          string `yaml:"test"`
	RequiredCheck string `yaml:"required_check"`
}

var (
	versionRangeRe = regexp.MustCompile(`^>=\d+\.\d+\.\d+$`)
	capTestRe      = regexp.MustCompile(`^[A-Za-z0-9_.-]+:Test[A-Za-z0-9_]+$`)
)

// LoadCertification reads and validates certification.yaml.
func LoadCertification(path string) ([]CertificationEntry, []error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, []error{cerrors.Errorf("read certification file: %w", err)}
	}
	var entries []CertificationEntry
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&entries); err != nil {
		return nil, []error{cerrors.Errorf("certification.yaml: %w", err)}
	}
	var problems []error
	seen := map[string]bool{}
	for i, e := range entries {
		where := fmt.Sprintf("certification.yaml[%d] (%s)", i, e.Plugin)
		switch {
		case e.Plugin == "":
			problems = append(problems, cerrors.New(where+": plugin is required"))
		case seen[NormalizePlugin(e.Plugin)]:
			problems = append(problems, cerrors.New(where+": duplicate plugin"))
		}
		seen[NormalizePlugin(e.Plugin)] = true
		if !versionRangeRe.MatchString(e.Version) {
			problems = append(problems, cerrors.New(where+": version must be >=X.Y.Z"))
		}
		if e.Tier != TierCertified && e.Tier != TierPending {
			problems = append(problems, cerrors.New(where+": tier must be certified or pending"))
		}
		if e.Lane <= 0 {
			problems = append(problems, cerrors.New(where+": lane (the owning issue number) is required"))
		}
		for name, c := range e.Capabilities {
			// Offline the runner checks only that the proving test is named and
			// well formed; the nightly checks that it exists and is required.
			if !capTestRe.MatchString(c.Test) || c.RequiredCheck == "" {
				problems = append(problems, cerrors.New(where+": capability "+name+" needs test <repo>:Test<Name> and required_check"))
			}
		}
	}
	return entries, problems
}

// NormalizePlugin reduces a plugin reference to the name certification
// entries are keyed by: the @version suffix is dropped and "standalone:" is
// implied, so builtin:postgres@v0.15.0 matches builtin:postgres and
// standalone:mysql@v0.2.0 matches mysql.
func NormalizePlugin(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	return strings.TrimPrefix(ref, "standalone:")
}

// CheckCertification enforces rule 4: every plugin migrate can emit has a
// certification entry. With release set, the entry must also be certified,
// which is the v0.21 release bar (#3002).
func CheckCertification(entries []CertificationEntry, plugins []string, release bool) []string {
	byName := map[string]CertificationEntry{}
	for _, e := range entries {
		byName[NormalizePlugin(e.Plugin)] = e
	}
	uniq := map[string]bool{}
	for _, p := range plugins {
		uniq[NormalizePlugin(p)] = true
	}
	names := make([]string, 0, len(uniq))
	for p := range uniq {
		names = append(names, p)
	}
	sort.Strings(names)
	var problems []string
	for _, p := range names {
		e, ok := byName[p]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("plugin %s can be emitted by migrate but has no certification.yaml entry", p))
		case release && e.Tier != TierCertified:
			problems = append(problems, fmt.Sprintf("plugin %s is tier %q in certification.yaml; the release gate requires certified (lane #%d)", p, e.Tier, e.Lane))
		}
	}
	return problems
}
