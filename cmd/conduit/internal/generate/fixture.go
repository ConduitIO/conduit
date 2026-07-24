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
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/yaml/v3"
)

// Request is one committed benchmark fixture: a canonical natural-language
// pipeline request paired with its expected semantic intent. This is the
// eval corpus's unit — see docs/design-documents/20260722-conduit-generate.md
// §10 and docs/generate-benchmark.md for the rendered corpus.
type Request struct {
	// ID is a stable, unique fixture identifier (kebab-case), used as the
	// key into a Candidates map and in test/report output. Renaming an ID
	// is a corpus-breaking change for anyone tracking scores over time —
	// treat it like the connector protocol's naming discipline.
	ID string `yaml:"id"`
	// Prompt is the exact natural-language request text a future `generate`
	// invocation (or a human reviewer of this corpus) would see verbatim.
	Prompt string `yaml:"prompt"`
	// Expect is the ground-truth semantic intent this request should
	// produce, judged by the deterministic heuristics in
	// semantic_score.go — never re-derived from Prompt by this package
	// (that NL-extraction heuristic is design doc §9's job, owned by the
	// future `generate` command itself, not by this scorer).
	Expect Expect `yaml:"expect"`
	// Notes is free-text context for a human reviewing the corpus (why this
	// fixture exists, what failure mode it targets). Never consumed by the
	// scorer.
	Notes string `yaml:"notes,omitempty"`
}

// Expect is a Request's ground-truth semantic intent: which built-in
// connector category (file, generator, kafka, log, postgres, or s3) should
// occupy the source and destination roles, and which processor
// capabilities (capability.go) the candidate must demonstrably include.
// SourceCategory/DestinationCategory are left empty only when a request
// deliberately doesn't pin one side down; every fixture in the committed
// corpus sets both, since "grounded in real built-in connectors" is the
// corpus's stated bar (task brief) and an unpinned side can't be scored as
// a mismatch.
type Expect struct {
	SourceCategory       string   `yaml:"sourceCategory"`
	DestinationCategory  string   `yaml:"destinationCategory"`
	RequiredCapabilities []string `yaml:"requiredCapabilities,omitempty"`
}

// LoadRequests reads and validates a committed eval corpus file (the shape
// testdata/eval_requests.yaml uses): every Request must carry a non-empty,
// unique ID and a non-empty Prompt. It never silently drops a malformed
// entry — a corpus fixture with a typo'd or duplicate ID is exactly the
// kind of silent drift that would make the reported rates measure a
// smaller (and differently-shaped) corpus than the one committed to the
// repo, so this is a hard error, not a skip.
func LoadRequests(path string) ([]Request, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, cerrors.Errorf("reading eval requests %q: %w", path, err)
	}

	var reqs []Request
	if err := yaml.Unmarshal(data, &reqs); err != nil {
		return nil, cerrors.Errorf("parsing eval requests %q: %w", path, err)
	}

	seen := make(map[string]bool, len(reqs))
	for i, r := range reqs {
		if r.ID == "" {
			return nil, cerrors.Errorf("eval requests %q: entry %d has no id", path, i)
		}
		if seen[r.ID] {
			return nil, cerrors.Errorf("eval requests %q: duplicate id %q", path, r.ID)
		}
		seen[r.ID] = true
		if r.Prompt == "" {
			return nil, cerrors.Errorf("eval requests %q: entry %q has no prompt", path, r.ID)
		}
	}

	return reqs, nil
}
