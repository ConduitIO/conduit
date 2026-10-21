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
	"fmt"
	"regexp"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/yaml/v3"
)

// Scopes of a rule file.
const (
	ScopeSource = "source" // one connector class whose connectors are sources
	ScopeSink   = "sink"   // one connector class whose connectors are sinks
	ScopeShared = "shared" // keys common to every connector class
	ScopeWorker = "worker" // worker properties
)

// PipelineNone marks a class that produces no pipeline.
const PipelineNone = "none"

// NoPipeline reports whether the class produces no pipeline.
func (f *File) NoPipeline() bool { return f.Pipeline == PipelineNone }

// Diff classes, consumed by `kc diff`.
const (
	DiffIdentity = "identity"
	DiffBehavior = "behavior"
	DiffTuning   = "tuning"
	DiffSecret   = "secret"
)

// File is one rule file. Unknown fields are rejected, so a typo in rule data
// fails the build instead of being ignored.
type File struct {
	RulesVersion int      `yaml:"rulesVersion"`
	Scope        string   `yaml:"scope"`
	Class        string   `yaml:"class"`
	Upstream     Upstream `yaml:"upstream"`
	// Emits lists every Conduit plugin this class can produce; the harness
	// requires a certification entry for each.
	Emits []Emit `yaml:"emits"`
	Keys  []Rule `yaml:"keys"`
	// Defaults are the upstream defaults Conduit does not share. Each one
	// adds a connector-level note when its key is absent, because an absent
	// key has no entry to carry the difference (mapping design, "Absent keys
	// are evaluated mechanically").
	Defaults []Default `yaml:"defaults"`
	// Notes are connector-level notes every connector of the class carries,
	// for differences that hold whatever the config says (an output shape
	// Conduit does not reproduce, for example).
	Notes []NoteSpec `yaml:"notes"`
	// Finish lists emit actions run once per pipeline after every key and
	// worker setting, for settings that depend on several keys and their
	// upstream defaults together (a topic naming policy, for example).
	Finish []Action `yaml:"finish"`
	// Pipeline is PipelineNone for a class that produces no pipeline
	// (MirrorMaker's heartbeat and checkpoint connectors). Such a file has
	// no keys and no emits; Every is the outcome of every key.
	Pipeline string `yaml:"pipeline"`
	// Every is the outcome of every key of a no-pipeline class.
	Every *Outcome `yaml:"every"`

	name     string // file name, for errors
	exact    map[string]*Rule
	prefixes []*Rule // longest first
	regexes  []*Rule
}

// Default describes an upstream default whose outcome is not plainly
// translated.
type Default struct {
	Key string `yaml:"key"`
	// Default is the upstream default value, as in the upstream snapshot.
	Default string `yaml:"default"`
	// Worker means a worker file can set the key too (converters); the note
	// is added only when neither the connector nor the worker sets it.
	Worker bool `yaml:"worker"`
	// Note is added when the key is absent.
	Note *NoteSpec `yaml:"note"`
	// CoveredBy names the code of another note of this file that already
	// covers the difference, so per-key notes would be noise.
	CoveredBy string `yaml:"coveredBy"`
}

// NoteSpec is a connector-level note in rule data.
type NoteSpec struct {
	Code     string `yaml:"code"`
	Reason   string `yaml:"reason"`
	Fix      string `yaml:"fix"`
	Blocking bool   `yaml:"blocking"`
}

// Upstream is the project and version the rules were written against.
type Upstream struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

// Emit is a Conduit plugin a class maps to.
type Emit struct {
	Plugin string `yaml:"plugin"`
	Role   string `yaml:"role"`
}

// Rule classifies the keys its Key matches.
type Rule struct {
	Key     KeyMatch `yaml:"key"`
	Outcome `yaml:",inline"`
	// Values gives per-value outcomes. The last branch must be the
	// "otherwise" branch, unsupported and blocking, so a value the rule does
	// not list (a new upstream enum member, a typo) is never accepted.
	Values []ValueBranch `yaml:"values"`
	// IgnoreCase makes Values match case-insensitively, for upstream enums
	// that are parsed that way.
	IgnoreCase bool `yaml:"ignoreCase"`
	// Emit names what a translated key becomes; kinds are code. The kind
	// "none" records a deliberate no-op.
	Emit *Action `yaml:"emit"`
	// Diff is the `kc diff` class of a change to this key.
	Diff string `yaml:"diff"`
	// Lint lists the `kc lint` rules that read this key.
	Lint []string `yaml:"lint"`
	// Secret marks the value as a secret whatever its name.
	Secret bool `yaml:"secret"`
	// AliasOf names the current name of a legacy key. The key is handled as
	// that key and additionally reports kc.key_deprecated.
	AliasOf string `yaml:"aliasOf"`

	re *regexp.Regexp
}

// Outcome is what a rule says about a key. It becomes a report entry.
type Outcome struct {
	Status   kcmigrate.Status `yaml:"status"`
	Blocking bool             `yaml:"blocking"`
	Code     string           `yaml:"code"`
	Reason   string           `yaml:"reason"`
	Fix      string           `yaml:"fix"`
}

// ValueBranch is the outcome for a set of values.
type ValueBranch struct {
	Match []string `yaml:"match"`
	// Pattern matches values with an RE2 regex, for value classes an enum
	// cannot list (a positive integer, a literal table list). It is anchored
	// by the rule author; Match is tried before Pattern.
	Pattern   string `yaml:"pattern"`
	Otherwise bool   `yaml:"otherwise"`
	Outcome   `yaml:",inline"`
	Emit      *Action `yaml:"emit"`

	re *regexp.Regexp
}

// Action is an emit kind and its arguments.
type Action struct {
	Kind string            `yaml:"kind"`
	Args map[string]string `yaml:",inline"`
}

// KeyMatch is a key pattern: an exact key (a YAML string), or a mapping with
// one of prefix or regex. Regexes are RE2, so matching is linear in the key.
type KeyMatch struct {
	Exact  string
	Prefix string
	Regex  string
}

// UnmarshalYAML reads a string as an exact key and a mapping as prefix or
// regex.
func (k *KeyMatch) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		k.Exact = n.Value
		return nil
	}
	var m struct {
		Prefix string `yaml:"prefix"`
		Regex  string `yaml:"regex"`
	}
	if err := n.Decode(&m); err != nil {
		return cerrors.Errorf("key: %w", err)
	}
	if (m.Prefix == "") == (m.Regex == "") {
		return cerrors.Errorf("line %d: key must be a string or have exactly one of prefix, regex", n.Line)
	}
	k.Prefix, k.Regex = m.Prefix, m.Regex
	return nil
}

func (k KeyMatch) String() string {
	switch {
	case k.Prefix != "":
		return "prefix " + k.Prefix
	case k.Regex != "":
		return "regex " + k.Regex
	default:
		return k.Exact
	}
}

// parseFile decodes and validates one rule file.
func parseFile(name string, b []byte) (*File, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, cerrors.Errorf("%s: %w", name, err)
	}
	f.name = name
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

func (f *File) validate() error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, cerrors.Errorf("%s: "+format, append([]any{f.name}, args...)...))
	}
	if f.RulesVersion != 1 {
		fail("rulesVersion must be 1")
	}
	switch f.Scope {
	case ScopeSource, ScopeSink:
		if f.Class == "" {
			fail("scope %s needs a class", f.Scope)
		}
	case ScopeShared, ScopeWorker:
		if f.Class != "" || len(f.Emits) > 0 {
			fail("scope %s has no class and no emits", f.Scope)
		}
	default:
		fail("unknown scope %q", f.Scope)
	}
	if f.Upstream.Name == "" || f.Upstream.Version == "" {
		fail("upstream needs name and version")
	}
	errs = append(errs, f.validatePipeline()...)
	seen := map[string]bool{}
	for i := range f.Keys {
		r := &f.Keys[i]
		id := r.Key.String()
		if seen[id] {
			fail("key %s has two rules", id)
		}
		seen[id] = true
		for _, err := range r.validate() {
			fail("key %s: %v", id, err)
		}
		for _, err := range r.validateBlockSinks(f.Scope) {
			fail("key %s: %v", id, err)
		}
	}
	errs = append(errs, f.validateDefaults()...)
	for _, r := range f.Keys {
		if r.AliasOf != "" && !seen[r.AliasOf] {
			fail("key %s: aliasOf %q has no exact rule in this file", r.Key, r.AliasOf)
		}
	}
	return cerrors.Join(errs...)
}

func (f *File) validateDefaults() []error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, cerrors.Errorf("%s: "+format, append([]any{f.name}, args...)...))
	}
	for _, n := range f.Notes {
		if !n.valid() {
			fail("note %s: a note needs a registered kc.* code, a reason, and a fix when blocking", n.Code)
		}
	}
	for _, a := range f.Finish {
		if a.Kind == "" {
			fail("a finish action needs a kind")
		}
	}
	noteCodes := map[string]bool{}
	for _, d := range f.Defaults {
		if d.Note != nil {
			noteCodes[d.Note.Code] = true
		}
	}
	for _, d := range f.Defaults {
		if d.Key == "" || (d.Note == nil) == (d.CoveredBy == "") {
			fail("default %q needs a key and exactly one of note, coveredBy", d.Key)
			continue
		}
		if d.CoveredBy != "" && !noteCodes[d.CoveredBy] {
			fail("default %s: coveredBy %q is not the code of a note in this file", d.Key, d.CoveredBy)
		}
		if n := d.Note; n != nil {
			if !n.valid() {
				fail("default %s: a note needs a registered kc.* code, a reason, and a fix when blocking", d.Key)
			}
		}
	}
	return errs
}

func (r *Rule) validate() []error {
	var errs []error
	if r.Key.Regex != "" {
		re, err := regexp.Compile(r.Key.Regex)
		if err != nil {
			errs = append(errs, cerrors.Errorf("regex: %w", err))
		}
		r.re = re
	}
	switch r.Diff {
	case DiffIdentity, DiffBehavior, DiffTuning, DiffSecret:
	default:
		errs = append(errs, cerrors.Errorf("diff must be identity, behavior, tuning or secret, not %q", r.Diff))
	}
	if r.AliasOf != "" {
		if r.Status != "" || len(r.Values) > 0 || r.Emit != nil {
			errs = append(errs, cerrors.New("an aliasOf rule takes its outcome from the aliased key"))
		}
		return errs
	}
	if len(r.Values) == 0 {
		return append(errs, r.Outcome.validate(r.Emit)...)
	}
	return append(errs, r.validateValues()...)
}

// validateBlockSinks checks the block-sinks emit kind, which only a worker
// rule may use, on an unsupported outcome: it scopes a worker key's block to
// the sink pipelines that read with the worker's consumer.
func (r *Rule) validateBlockSinks(scope string) []error {
	var errs []error
	check := func(o Outcome, a *Action) {
		if a == nil || a.Kind != BlockSinksKind {
			return
		}
		if scope != ScopeWorker {
			errs = append(errs, cerrors.New(BlockSinksKind+" is only for worker rule files"))
		}
		if o.Status != kcmigrate.StatusUnsupported {
			errs = append(errs, cerrors.New(BlockSinksKind+" is only for an unsupported outcome"))
		}
	}
	if len(r.Values) == 0 {
		check(r.Outcome, r.Emit)
	}
	for _, b := range r.Values {
		check(b.Outcome, branchEmit(r, b))
	}
	return errs
}

func (r *Rule) validateValues() []error {
	var errs []error
	if r.Outcome != (Outcome{}) {
		errs = append(errs, cerrors.New("a rule with values sets its outcome per value"))
	}
	values := map[string]bool{}
	for i := range r.Values {
		errs = append(errs, r.validateBranch(i, values)...)
	}
	return errs
}

func (r *Rule) validateBranch(i int, values map[string]bool) []error {
	var errs []error
	b := r.Values[i]
	last := i == len(r.Values)-1
	switch {
	case b.Otherwise && !last, !b.Otherwise && last:
		errs = append(errs, cerrors.New("values must end with exactly one otherwise branch"))
	case b.Otherwise && (b.Status != kcmigrate.StatusUnsupported || !b.Blocking || b.Code != kcmigrate.CodeKeyUnsupported.Reason()):
		errs = append(errs, cerrors.New("the otherwise branch must be unsupported, blocking, kc.key_unsupported"))
	case !b.Otherwise && len(b.Match) == 0 && b.Pattern == "":
		errs = append(errs, cerrors.New("a value branch needs match or pattern"))
	case b.Otherwise && (len(b.Match) > 0 || b.Pattern != ""):
		errs = append(errs, cerrors.New("the otherwise branch matches everything else and takes no match or pattern"))
	}
	if b.Pattern != "" {
		re, err := regexp.Compile(b.Pattern)
		if err != nil {
			errs = append(errs, cerrors.Errorf("pattern: %w", err))
		}
		r.Values[i].re = re
	}
	for _, v := range b.Match {
		if r.IgnoreCase {
			v = strings.ToLower(v)
		}
		if values[v] {
			errs = append(errs, cerrors.Errorf("value %q is in two branches", v))
		}
		values[v] = true
	}
	return append(errs, b.Outcome.validate(branchEmit(r, b))...)
}

// validatePipeline checks a no-pipeline class: one outcome for every key,
// unsupported (nothing is written for any key), and nothing to emit.
func (f *File) validatePipeline() []error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, cerrors.Errorf("%s: "+format, append([]any{f.name}, args...)...))
	}
	switch f.Pipeline {
	case "":
		if f.Every != nil {
			fail("every needs pipeline: none")
		}
		return errs
	case PipelineNone:
	default:
		fail("pipeline must be absent or none, not %q", f.Pipeline)
		return errs
	}
	if f.Scope != ScopeSource && f.Scope != ScopeSink {
		fail("pipeline: none needs scope source or sink")
	}
	if e := f.Every; e == nil {
		fail("pipeline: none needs every")
	} else if e.Status != kcmigrate.StatusUnsupported || e.Reason == "" || (e.Blocking && e.Fix == "") || !strings.HasPrefix(e.Code, "kc.") {
		fail("every must be unsupported, with a kc.* code, a reason, and a fix when blocking")
	} else if _, ok := conduiterr.LookupCode(e.Code); !ok {
		fail("every: code %q is not registered", e.Code)
	}
	if len(f.Keys) > 0 || len(f.Defaults) > 0 {
		fail("pipeline: none has no keys and no defaults: every is the outcome of every key")
	}
	if len(f.Emits) > 0 || len(f.Finish) > 0 {
		fail("pipeline: none has no emits and no finish actions")
	}
	return errs
}

func (o Outcome) validate(emit *Action) []error {
	var errs []error
	switch o.Status {
	case kcmigrate.StatusTranslated:
		if emit == nil || emit.Kind == "" {
			errs = append(errs, cerrors.New("a translated outcome names an emit kind (none for a no-op)"))
		}
	case kcmigrate.StatusApproximated, kcmigrate.StatusUnsupported:
		if o.Code == "" || o.Reason == "" {
			errs = append(errs, cerrors.Errorf("a %s outcome needs code and reason", o.Status))
		}
	default:
		errs = append(errs, cerrors.Errorf("unknown status %q", o.Status))
	}
	if (o.Status == kcmigrate.StatusUnsupported || o.Blocking) && o.Fix == "" {
		errs = append(errs, cerrors.New("an unsupported or blocking outcome needs a fix"))
	}
	if o.Code != "" {
		if _, ok := conduiterr.LookupCode(o.Code); !ok || !strings.HasPrefix(o.Code, "kc.") {
			errs = append(errs, cerrors.Errorf("code %q is not a registered kc.* code", o.Code))
		}
	}
	return errs
}

func (o Outcome) entry(key string, origin kcmigrate.Origin) kcmigrate.Entry {
	return kcmigrate.Entry{
		Key: key, Status: o.Status, Origin: origin,
		Code: o.Code, Reason: o.Reason, Fix: o.Fix, Blocking: o.Blocking,
	}
}

func (f *File) info() kcmigrate.RulesInfo {
	class := f.Class
	if class == "" {
		class = "(" + f.Scope + ")"
	}
	return kcmigrate.RulesInfo{
		Class: class, RulesVersion: f.RulesVersion,
		Upstream: kcmigrate.Upstream{Name: f.Upstream.Name, Version: f.Upstream.Version},
	}
}

func (f *File) String() string { return fmt.Sprintf("%s (%s)", f.name, f.Scope) }

// Name is the rule file's name.
func (f *File) Name() string { return f.name }

func (n NoteSpec) valid() bool {
	_, ok := conduiterr.LookupCode(n.Code)
	return ok && strings.HasPrefix(n.Code, "kc.") && n.Reason != "" && (!n.Blocking || n.Fix != "")
}
