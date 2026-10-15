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
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/yaml/v3"
)

// FamilyCommon holds cases that test parsers, secrets, SMTs, converters
// and unsupported classes rather than one connector family.
const FamilyCommon = "common"

// Families are the corpus families of the design, in its order.
var Families = []string{
	"debezium-postgres", "debezium-mysql", "jdbc-source", "jdbc-sink",
	"s3-sink", "elasticsearch-sink", "kafka-mirror", FamilyCommon,
}

// AllowedLicenses are the SPDX ids a corpus input may be taken under. All
// are compatible with redistribution in this Apache-2.0 repository. The
// Confluent Community License (the license of the Confluent connector
// repositories themselves) is deliberately not on the list.
var AllowedLicenses = []string{"Apache-2.0", "MIT", "MIT-0", "BSD-2-Clause", "BSD-3-Clause", "CC0-1.0"}

// Command-line words the harness uses.
const (
	cmdMigrate      = "migrate"
	cmdKafkaConnect = "kafka-connect"
	flagJSON        = "--json"
)

// Status values of a report entry (base design, "Output: the compatibility report").
const (
	StatusTranslated   = "translated"
	StatusApproximated = "approximated"
	StatusUnsupported  = "unsupported"
)

var (
	caseIDRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	commitRe   = regexp.MustCompile(`/[0-9a-f]{40}/`)
	codeRe     = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_-]+)+$`)
	inputFlags = map[string]bool{"--worker": true}
	// runnerFlags are added by the runner; a case must not set them.
	runnerFlags = []string{flagJSON, "--out", "--force", "--quiet", "--no-color"}
)

// Manifest is case.yaml. Fields beyond the design's sketch are additive and
// marked: provenance.license/copyright/upstreamPath/modifications,
// expect.entries, expect.error and expect.pipelines.
type Manifest struct {
	SchemaVersion int               `yaml:"schemaVersion"`
	ID            string            `yaml:"id"`
	Family        string            `yaml:"family"`
	Description   string            `yaml:"description"`
	Provenance    Provenance        `yaml:"provenance"`
	Sanitized     Sanitized         `yaml:"sanitized"`
	Upstream      map[string]string `yaml:"upstream"`
	Command       Command           `yaml:"command"`
	Expect        Expect            `yaml:"expect"`
	ExpectUnknown []string          `yaml:"expectUnknown"`
	Covers        []Cover           `yaml:"covers"`
	Emits         []Emit            `yaml:"emits"`
	Run           Run               `yaml:"run"`
}

type Provenance struct {
	Source        string   `yaml:"source"` // vendor-doc | design-partner | synthetic
	Ref           string   `yaml:"ref"`
	License       string   `yaml:"license"`       // additive: SPDX id
	Copyright     string   `yaml:"copyright"`     // additive
	Modifications []string `yaml:"modifications"` // additive: Apache-2.0 §4(b) change notice
}

type Sanitized struct {
	Scrubbed bool   `yaml:"scrubbed"`
	Check    string `yaml:"check"`
}

type Command struct {
	Args []string `yaml:"args"`
}

// Expect holds the outcomes a case asserts independently of its goldens.
// A nil field is not asserted.
type Expect struct {
	ExitCode  *int          `yaml:"exitCode"`
	Counts    *Counts       `yaml:"counts"`
	Notes     []string      `yaml:"notes"`     // codes that must be among the connector-level notes
	Entries   []EntryExpect `yaml:"entries"`   // additive
	Error     string        `yaml:"error"`     // additive: the envelope error code of a rejected input
	Pipelines *int          `yaml:"pipelines"` // additive: number of pipeline files written
}

type Counts struct {
	Translated   int `yaml:"translated"`
	Approximated int `yaml:"approximated"`
	Unsupported  int `yaml:"unsupported"`
	Blocking     int `yaml:"blocking"`
}

// EntryExpect asserts the classification of every report entry for Key.
type EntryExpect struct {
	Key      string `yaml:"key"`
	Status   string `yaml:"status"`
	Blocking *bool  `yaml:"blocking"`
	Code     string `yaml:"code"`
	Secret   *bool  `yaml:"secret"`
}

type Cover struct {
	Class string   `yaml:"class"`
	Keys  []string `yaml:"keys"`
}

type Emit struct {
	Plugin string `yaml:"plugin"`
	Role   string `yaml:"role"`
}

type Run struct {
	Tier string `yaml:"tier"` // offline | compose | debezium
}

// Case is one corpus case on disk.
type Case struct {
	// Dir is the absolute case directory.
	Dir string
	// Rel is the case directory relative to the corpus root, slash separated
	// (<family>/<id>).
	Rel      string
	Manifest Manifest
}

// Inputs returns the input files the command line names, relative to the
// case directory, in command-line order (--worker first if it comes first).
func (c Case) Inputs() []string {
	var in []string
	for _, a := range c.Manifest.Command.Args {
		if strings.HasPrefix(a, "input/") {
			in = append(in, a)
		}
	}
	return in
}

// LoadCorpus loads every <family>/<case>/case.yaml under root. It returns the
// cases it could parse and every problem it found; a non-empty problem list
// fails the corpus.
func LoadCorpus(root string) ([]Case, []error) {
	var cases []Case
	var problems []error
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, []error{cerrors.Errorf("corpus root: %w", err)}
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "case.yaml" {
			return nil
		}
		dir := filepath.Dir(p)
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return err //nolint:wrapcheck // walk error, reported once below
		}
		c, errs := loadCase(dir, filepath.ToSlash(rel))
		problems = append(problems, errs...)
		if c != nil {
			cases = append(cases, *c)
		}
		return nil
	})
	if err != nil {
		problems = append(problems, cerrors.Errorf("walk corpus: %w", err))
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Rel < cases[j].Rel })
	return cases, problems
}

func loadCase(dir, rel string) (*Case, []error) {
	b, err := os.ReadFile(filepath.Join(dir, "case.yaml"))
	if err != nil {
		return nil, []error{cerrors.Errorf("%s: %w", rel, err)}
	}
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, []error{cerrors.Errorf("%s/case.yaml: %w", rel, err)}
	}
	c := &Case{Dir: dir, Rel: rel, Manifest: m}
	var errs []error
	for _, p := range validateManifest(c) {
		errs = append(errs, cerrors.New(rel+"/case.yaml: "+p))
	}
	return c, errs
}

//nolint:gocyclo // a flat list of independent field checks reads best as one function
func validateManifest(c *Case) []string {
	m := c.Manifest
	var p []string
	add := func(format string, args ...any) { p = append(p, fmt.Sprintf(format, args...)) }

	family, id := path.Split(c.Rel)
	family = strings.TrimSuffix(family, "/")
	if m.SchemaVersion != 1 {
		add("schemaVersion must be 1, got %d", m.SchemaVersion)
	}
	if m.ID != id || !caseIDRe.MatchString(m.ID) {
		add("id %q must equal the directory name %q and match %s", m.ID, id, caseIDRe)
	}
	if m.Family != family || !slices.Contains(Families, m.Family) {
		add("family %q must equal the parent directory %q and be one of %v", m.Family, family, Families)
	}
	if strings.TrimSpace(m.Description) == "" {
		add("description is required")
	}

	switch m.Provenance.Source {
	case "vendor-doc":
		if !commitRe.MatchString(m.Provenance.Ref) {
			add("provenance.ref of a vendor-doc case must be a URL pinned to a 40-hex commit")
		}
		if m.Provenance.Copyright == "" {
			add("provenance.copyright is required for vendor-doc cases")
		}
	case "design-partner":
		if m.Provenance.Ref == "" {
			add("provenance.ref (partner alias) is required")
		}
	case "synthetic":
	default:
		add("provenance.source must be vendor-doc, design-partner or synthetic, got %q", m.Provenance.Source)
	}
	if !slices.Contains(AllowedLicenses, m.Provenance.License) {
		add("provenance.license %q is not in the allowed list %v", m.Provenance.License, AllowedLicenses)
	}
	if m.Provenance.Source != "synthetic" && len(m.Provenance.Modifications) == 0 {
		add("provenance.modifications must list the changes made to the upstream file (at least the scrub)")
	}
	if !m.Sanitized.Scrubbed || m.Sanitized.Check != ScrubCheck {
		add("sanitized must be {scrubbed: true, check: %s}", ScrubCheck)
	}

	args := m.Command.Args
	if len(args) < 3 || args[0] != cmdMigrate || args[1] != cmdKafkaConnect {
		add("command.args must start with [migrate, kafka-connect] and name at least one input")
	}
	for i, a := range args {
		for _, f := range runnerFlags {
			if a == f || strings.HasPrefix(a, f+"=") {
				add("command.args must not set %s; the runner adds it", f)
			}
		}
		if inputFlags[a] && (i+1 >= len(args) || !strings.HasPrefix(args[i+1], "input/")) {
			add("%s must be followed by an input/ path", a)
		}
	}
	inputs := c.Inputs()
	if len(inputs) == 0 {
		add("command.args names no input/ file")
	}
	for _, in := range inputs {
		if strings.Contains(in, "..") || path.Clean(in) != in {
			add("input path %q is not clean", in)
			continue
		}
		ext := path.Ext(in)
		if ext != ".json" && ext != ".properties" {
			add("input %q: the harness reads .json and .properties inputs only", in)
		}
		if _, err := os.Stat(filepath.Join(c.Dir, filepath.FromSlash(in))); err != nil {
			add("input %q does not exist", in)
		}
	}
	if onDisk, err := listFiles(filepath.Join(c.Dir, "input")); err == nil {
		for _, f := range onDisk {
			if !slices.Contains(inputs, "input/"+f) {
				add("input/%s is not named on the command line; every input file must be used", f)
			}
		}
	}

	e := m.Expect
	if e.ExitCode != nil && !slices.Contains([]int{0, 1, 2, 3}, *e.ExitCode) {
		add("expect.exitCode %d is not an exit code migrate can return (0, 1, 2, 3)", *e.ExitCode)
	}
	if e.Error != "" {
		if !codeRe.MatchString(e.Error) {
			add("expect.error %q is not a domain.reason code", e.Error)
		}
		if len(e.Entries) > 0 || e.Counts != nil || len(e.Notes) > 0 {
			add("expect.error excludes entries, counts and notes: a rejected input has no report")
		}
	}
	for _, en := range e.Entries {
		if en.Key == "" {
			add("expect.entries: key is required")
		}
		if !slices.Contains([]string{StatusTranslated, StatusApproximated, StatusUnsupported}, en.Status) {
			add("expect.entries[%s]: status %q is not translated, approximated or unsupported", en.Key, en.Status)
		}
		if en.Code != "" && !codeRe.MatchString(en.Code) {
			add("expect.entries[%s]: code %q is not a domain.reason code", en.Key, en.Code)
		}
	}
	for _, n := range e.Notes {
		if !codeRe.MatchString(n) {
			add("expect.notes: %q is not a domain.reason code", n)
		}
	}

	if m.Family != FamilyCommon && len(m.Covers) == 0 && e.Error == "" {
		add("covers must name the rule keys this case exercises (rule 3)")
	}
	for _, cv := range m.Covers {
		if cv.Class == "" || len(cv.Keys) == 0 {
			add("covers entries need a class and at least one key")
		}
	}
	for _, em := range m.Emits {
		if em.Plugin == "" || (em.Role != "source" && em.Role != "destination") {
			add("emits entry %+v needs a plugin and role source|destination", em)
		}
	}
	switch m.Run.Tier {
	case "offline":
	case "compose", "debezium":
		if _, err := os.Stat(filepath.Join(c.Dir, "oracle")); err != nil {
			add("run.tier %s requires an oracle/ directory", m.Run.Tier)
		}
	default:
		add("run.tier must be offline, compose or debezium, got %q", m.Run.Tier)
	}
	return p
}

func listFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err //nolint:wrapcheck // returned by the caller as-is
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err //nolint:wrapcheck // callers only check for existence
}
