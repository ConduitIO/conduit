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

package migrate

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/conduitio/conduit/tests/migrate/harness"
)

const corpusRoot = "corpus"

// releaseMode is the v0.21 release gate (#3002): certification entries must
// be certified, not pending. Wave 3 sets it; per-PR runs do not.
func releaseMode() bool { return os.Getenv("MIGRATE_HARNESS_RELEASE") == "1" }

func loadCorpus(t *testing.T) []harness.Case {
	t.Helper()
	cases, problems := harness.LoadCorpus(corpusRoot)
	for _, p := range problems {
		t.Error(p)
	}
	if len(cases) == 0 {
		t.Fatal("corpus has no cases")
	}
	return cases
}

func TestCorpus_Manifests(t *testing.T) {
	cases := loadCorpus(t)
	seen := map[string]string{}
	for _, c := range cases {
		if prev, ok := seen[c.Manifest.ID]; ok {
			t.Errorf("case id %q is used by %s and %s", c.Manifest.ID, prev, c.Rel)
		}
		seen[c.Manifest.ID] = c.Rel
	}
}

// TestCorpus_Inputs checks that every input parses with the harness's own
// parsers, and that what a manifest says about its input is true of it: a
// case cannot cover, expect or tolerate as unknown a key its input lacks.
func TestCorpus_Inputs(t *testing.T) {
	for _, c := range loadCorpus(t) {
		t.Run(c.Rel, func(t *testing.T) {
			files, err := harness.InputKeys(c.Dir, c.Inputs())
			if err != nil {
				t.Fatal(err)
			}
			present := map[string]bool{}
			for _, f := range files {
				if f.Shape == harness.ShapeUnrecognized && c.Manifest.Expect.Error == "" {
					t.Errorf("%s: no connector recognized in the input; a case like this must set expect.error", f.Path)
				}
				if len(f.Keys) == 0 {
					t.Errorf("%s: no keys", f.Path)
				}
				for _, k := range f.Keys {
					present[k.Key] = true
				}
			}
			for _, e := range c.Manifest.Expect.Entries {
				if !present[e.Key] {
					t.Errorf("expect.entries names %q, which is not in the input", e.Key)
				}
			}
			for _, k := range c.Manifest.ExpectUnknown {
				if !present[k] {
					t.Errorf("expectUnknown names %q, which is not in the input", k)
				}
			}
			for _, cv := range c.Manifest.Covers {
				for _, k := range cv.Keys {
					if !present[k] {
						t.Errorf("covers names %q, which is not in the input", k)
					}
				}
			}
		})
	}
}

// TestCorpus_Scrub is scrub-v1 (rule 7) over every input file: the
// parsed-value check and the raw-bytes scan of the whole file.
func TestCorpus_Scrub(t *testing.T) {
	for _, c := range loadCorpus(t) {
		files, err := harness.InputKeys(c.Dir, c.Inputs())
		if err != nil {
			t.Errorf("%s: %v", c.Rel, err)
			continue
		}
		for _, f := range harness.Scrub(files) {
			t.Errorf("%s: %s", c.Rel, f)
		}
		for _, in := range c.Inputs() {
			b, err := os.ReadFile(filepath.Join(c.Dir, filepath.FromSlash(in)))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range harness.ScrubRaw(in, b) {
				t.Errorf("%s: %s", c.Rel, f)
			}
		}
	}
}

// deniedRepoRe are repositories whose license is not on the allow-list,
// whatever a case declares: the Confluent connector repositories are under
// the Confluent Community License.
var deniedRepoRe = regexp.MustCompile(`^confluentinc/kafka-connect-`)

var githubRepoRe = regexp.MustCompile(`https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/blob/`)

// TestCorpus_Attribution requires every repository a case was taken from to
// be listed in ATTRIBUTION.md with the license the case declares.
func TestCorpus_Attribution(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(corpusRoot, "ATTRIBUTION.md"))
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{} // repo -> license column
	for _, line := range strings.Split(string(b), "\n") {
		cols := strings.Split(line, "|")
		if len(cols) < 4 {
			continue
		}
		if m := regexp.MustCompile(`github\.com/([^>/]+/[^>/]+)>`).FindStringSubmatch(cols[1]); m != nil {
			rows[m[1]] = strings.TrimSpace(cols[2])
		}
	}
	for _, c := range loadCorpus(t) {
		if c.Manifest.Provenance.Source != "vendor-doc" {
			continue
		}
		repos := githubRepoRe.FindAllStringSubmatch(c.Manifest.Provenance.Ref, -1)
		if len(repos) == 0 {
			t.Errorf("%s: provenance.ref names no GitHub blob URL", c.Rel)
		}
		for _, m := range repos {
			if deniedRepoRe.MatchString(m[1]) {
				t.Errorf("%s: %s is under the Confluent Community License and may not be used", c.Rel, m[1])
				continue
			}
			lic, ok := rows[m[1]]
			switch {
			case !ok:
				t.Errorf("%s: repository %s is not listed in ATTRIBUTION.md", c.Rel, m[1])
			case lic != c.Manifest.Provenance.License:
				t.Errorf("%s: license %s differs from ATTRIBUTION.md's %s for %s", c.Rel, c.Manifest.Provenance.License, lic, m[1])
			}
		}
	}
}

// TestCorpus_Certification is the offline half of rule 4: every plugin a
// case declares in emits has a certification entry. The runner adds the
// plugins the generated pipelines actually reference.
func TestCorpus_Certification(t *testing.T) {
	entries, problems := harness.LoadCertification(filepath.Join(corpusRoot, "certification.yaml"))
	for _, p := range problems {
		t.Error(p)
	}
	var plugins []string
	for _, c := range loadCorpus(t) {
		for _, e := range c.Manifest.Emits {
			plugins = append(plugins, e.Plugin)
		}
	}
	for _, p := range harness.CheckCertification(entries, plugins, releaseMode()) {
		t.Error(p)
	}
}

func TestCorpus_Coverage(t *testing.T) {
	cases := loadCorpus(t)
	_, problems := harness.LoadCoverage(filepath.Join(corpusRoot, "coverage.yaml"), cases)
	for _, p := range problems {
		t.Error(p)
	}
	// The six families #3010 names must each have at least one case.
	for _, f := range []string{"debezium-postgres", "debezium-mysql", "jdbc-sink", "s3-sink", "elasticsearch-sink", "kafka-mirror"} {
		if !slices.ContainsFunc(cases, func(c harness.Case) bool { return c.Manifest.Family == f }) {
			t.Errorf("family %s has no case", f)
		}
	}
}
