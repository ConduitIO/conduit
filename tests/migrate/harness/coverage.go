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
	"os"
	"slices"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/yaml/v3"
)

// runNone marks a family with no runnable tier yet.
const runNone = "none"

// Coverage is corpus/coverage.yaml.
type Coverage struct {
	SchemaVersion int                       `yaml:"schemaVersion"`
	Families      map[string]FamilyCoverage `yaml:"families"`
	Uncovered     []UncoveredKey            `yaml:"uncovered"`
}

// FamilyCoverage states which runner tiers a family has.
type FamilyCoverage struct {
	Run    string `yaml:"run"` // offline | compose | none
	Reason string `yaml:"reason"`
}

// UncoveredKey is a rule-file key no case covers, with the reason.
type UncoveredKey struct {
	Class  string `yaml:"class"`
	Key    string `yaml:"key"`
	Reason string `yaml:"reason"`
}

// LoadCoverage reads and validates coverage.yaml against the corpus cases.
func LoadCoverage(path string, cases []Case) (Coverage, []error) {
	var cov Coverage
	b, err := os.ReadFile(path)
	if err != nil {
		return cov, []error{cerrors.Errorf("read coverage file: %w", err)}
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&cov); err != nil {
		return cov, []error{cerrors.Errorf("coverage.yaml: %w", err)}
	}
	var problems []error
	if cov.SchemaVersion != 1 {
		problems = append(problems, cerrors.New("coverage.yaml: schemaVersion must be 1"))
	}
	for _, f := range Families {
		fc, ok := cov.Families[f]
		if !ok {
			problems = append(problems, cerrors.New("coverage.yaml: family "+f+" is not declared"))
			continue
		}
		if !slices.Contains([]string{"offline", "compose", runNone}, fc.Run) {
			problems = append(problems, cerrors.New("coverage.yaml: family "+f+": run must be offline, compose or none"))
		}
		if fc.Run == runNone && fc.Reason == "" {
			problems = append(problems, cerrors.New("coverage.yaml: family "+f+": run none needs a reason"))
		}
		hasCase := slices.ContainsFunc(cases, func(c Case) bool { return c.Manifest.Family == f })
		if fc.Run != runNone && !hasCase {
			problems = append(problems, cerrors.New("coverage.yaml: family "+f+" declares run "+fc.Run+" but has no case"))
		}
		if fc.Run == runNone && hasCase {
			problems = append(problems, cerrors.New("coverage.yaml: family "+f+" declares run none but has cases"))
		}
	}
	for k := range cov.Families {
		if !slices.Contains(Families, k) {
			problems = append(problems, cerrors.New("coverage.yaml: unknown family "+k))
		}
	}
	for _, u := range cov.Uncovered {
		if u.Class == "" || u.Key == "" || u.Reason == "" {
			problems = append(problems, cerrors.New("coverage.yaml: uncovered entries need class, key and reason"))
		}
	}
	return cov, problems
}
