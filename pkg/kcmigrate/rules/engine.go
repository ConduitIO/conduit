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
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/kcmigrate"
)

// Engine classifies connector and worker keys with a set of rule files.
// It is immutable after Load and safe for concurrent use.
type Engine struct {
	files   []*File // sorted by class, then scope
	classes map[string]*File
	shared  *File
	worker  *File
}

// Load reads and validates every *.yaml file at the root of fsys. Any
// invalid file, two files for one class, or two shared or worker files fail
// the load: rule data errors are build errors, caught by tests.
func Load(fsys fs.FS) (*Engine, error) {
	names, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		return nil, cerrors.Errorf("list rule files: %w", err)
	}
	sort.Strings(names)
	e := &Engine{classes: map[string]*File{}}
	var errs []error
	for _, name := range names {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, cerrors.Errorf("read %s: %w", name, err)
		}
		f, err := parseFile(name, b)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		f.index()
		if err := f.checkRegexOverlap(); err != nil {
			errs = append(errs, err)
		}
		switch f.Scope {
		case ScopeShared, ScopeWorker:
			slot := &e.shared
			if f.Scope == ScopeWorker {
				slot = &e.worker
			}
			if *slot != nil {
				errs = append(errs, cerrors.Errorf("%s and %s both have scope %s", (*slot).name, name, f.Scope))
			}
			*slot = f
		default:
			if other, ok := e.classes[f.Class]; ok {
				errs = append(errs, cerrors.Errorf("%s and %s both have class %s", other.name, name, f.Class))
			}
			e.classes[f.Class] = f
		}
		e.files = append(e.files, f)
	}
	if err := cerrors.Join(errs...); err != nil {
		return nil, err
	}
	sort.SliceStable(e.files, func(i, j int) bool { return e.files[i].info().Class < e.files[j].info().Class })
	return e, nil
}

// Rules describes every loaded rule file, for the report's generator.
func (e *Engine) Rules() []kcmigrate.RulesInfo {
	out := make([]kcmigrate.RulesInfo, 0, len(e.files))
	for _, f := range e.files {
		out = append(out, f.info())
	}
	return out
}

// Decision is the engine's verdict on one key occurrence: its report entry
// and, when the key is translated or approximated into a setting, the emit
// action. Emitters may refine Entry (targets, secrets), never drop it.
type Decision struct {
	Entry kcmigrate.Entry
	KV    kcmigrate.KV
	// Emit is nil when nothing is emitted: unsupported keys, overridden
	// duplicates, unknown keys and unknown classes.
	Emit *Action
	// Secret is the rule's secret flag.
	Secret bool
}

// Classified is a connector after classification.
type Classified struct {
	Connector kcmigrate.ReportConnector
	Decisions []Decision // one per KV, in input order; Connector.Entries mirrors them
	// Rules is the class's rule file, nil for an unknown class.
	Rules *File
}

// Classify classifies every key occurrence of c, in input order. A class
// with no rule file yields the role unknown and one blocking
// kc.connector_class_unsupported entry per key. Otherwise each key is looked
// up in the class's rules, then in the shared rules; a key no rule claims is
// kc.key_unknown, blocking. Notes, transforms and pipeline targets are left
// to the emitters.
func (e *Engine) Classify(c kcmigrate.Connector) Classified {
	rc := kcmigrate.ReportConnector{
		Name: c.Name, Class: c.Class, Notes: []kcmigrate.Note{}, Transforms: []kcmigrate.Transform{},
	}
	f, ok := e.classes[c.Class]
	var ds []Decision
	if ok {
		rc.Role = kcmigrate.Role(f.Scope)
		ds = classifyAll(c.Config, f, e.shared)
	} else {
		rc.Role, f = kcmigrate.RoleUnknown, nil
		for _, kv := range c.Config {
			ds = append(ds, Decision{Entry: classUnsupported(c.Class).entry(kv.Key, kv.Origin), KV: kv})
		}
	}
	rc.Entries = Entries(ds)
	return Classified{Connector: rc, Decisions: ds, Rules: f}
}

// Connector is Classify without the decisions.
func (e *Engine) Connector(c kcmigrate.Connector) kcmigrate.ReportConnector {
	return e.Classify(c).Connector
}

// Worker classifies a worker file's keys with the worker rules. The report
// element's name is the file label.
func (e *Engine) Worker(w kcmigrate.Worker) ([]Decision, kcmigrate.ReportConnector) {
	ds := classifyAll(w.Props, e.worker, nil)
	return ds, kcmigrate.ReportConnector{
		Name: w.File, Role: kcmigrate.RoleWorker,
		Entries: Entries(ds), Notes: []kcmigrate.Note{}, Transforms: []kcmigrate.Transform{},
	}
}

// Entries returns the entries of ds, in order. It always returns a non-nil
// slice.
func Entries(ds []Decision) []kcmigrate.Entry {
	out := make([]kcmigrate.Entry, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Entry)
	}
	return out
}

// classifyAll is where the completeness invariant is upheld: exactly one
// decision per KV, in input order, whatever the rules say.
func classifyAll(kvs []kcmigrate.KV, primary, fallback *File) []Decision {
	last := map[string]int{}
	for i, kv := range kvs {
		last[kv.Key] = i
	}
	out := make([]Decision, 0, len(kvs))
	for i, kv := range kvs {
		if j := last[kv.Key]; j != i {
			out = append(out, Decision{Entry: duplicate(kvs[j].Origin).entry(kv.Key, kv.Origin), KV: kv})
			continue
		}
		out = append(out, classify(kv, primary, fallback))
	}
	return out
}

func classify(kv kcmigrate.KV, primary, fallback *File) Decision {
	// The more specific of the class's and the shared rule wins (exact over
	// prefix over regex, the longer prefix over the shorter), and the class
	// on a tie, so a class prefix such as MirrorMaker's producer. does not
	// shadow a shared exact rule such as producer.override.acks.
	home := primary
	r := home.match(kv.Key)
	if fr := fallback.match(kv.Key); fr != nil && specificity(fr) > specificity(r) {
		home, r = fallback, fr
	}
	if r == nil {
		return Decision{Entry: unknownKey(kv.Key, primary).entry(kv.Key, kv.Origin), KV: kv}
	}
	alias := r.AliasOf
	if alias != "" {
		r = home.exact[alias]
	}
	o, act := r.outcomeFor(kv.Value)
	d := Decision{Entry: o.entry(kv.Key, kv.Origin), KV: kv, Emit: act, Secret: r.Secret}
	if alias != "" {
		legacy := fmt.Sprintf("%s is the legacy name of %s and is handled as %s", kv.Key, alias, alias)
		if d.Entry.Status == kcmigrate.StatusTranslated {
			d.Entry.Status, d.Entry.Code = kcmigrate.StatusApproximated, kcmigrate.CodeKeyDeprecated.Reason()
			d.Entry.Reason, d.Entry.Fix = legacy+".", "Rename "+kv.Key+" to "+alias+"."
		} else {
			d.Entry.Reason += " (" + legacy + ".)"
		}
	}
	return d
}

// referencePattern is Kafka Connect's ConfigTransformer pattern for
// config-provider references such as ${file:/path:key} and ${env:NAME}.
var referencePattern = regexp.MustCompile(`\$\{([^}]*?):(([^}]*?):)?([^}]*?)\}`)

// IsReference reports whether v contains a config-provider reference.
func IsReference(v string) bool { return referencePattern.MatchString(v) }

// outcomeFor returns the outcome for value v and the action to emit, nil
// for an unsupported outcome.
func (r *Rule) outcomeFor(v kcmigrate.Value) (Outcome, *Action) {
	if len(r.Values) == 0 {
		return r.Outcome, actionFor(r.Outcome, r.Emit)
	}
	// Compared only, never copied into the outcome. Trimmed as Kafka
	// Connect's ConfigDef.parseType trims every string-typed value.
	raw := strings.TrimSpace(v.Reveal())
	if IsReference(raw) {
		return Outcome{
			Status: kcmigrate.StatusUnsupported, Blocking: true, Code: kcmigrate.CodeKeyUnsupported.Reason(),
			Reason: "The value is a config-provider reference. How this key is migrated depends on its value, which migrate cannot read offline.",
			Fix:    "Set the literal value in the Kafka Connect config, or resolve the reference and re-run.",
		}, nil
	}
	branches := r.Values[:len(r.Values)-1]
	for _, b := range branches { // exact values first, then patterns
		for _, m := range b.Match {
			if m == raw || (r.IgnoreCase && strings.EqualFold(m, raw)) {
				return b.Outcome, actionFor(b.Outcome, branchEmit(r, b))
			}
		}
	}
	for _, b := range branches {
		if b.re != nil && b.re.MatchString(raw) {
			return b.Outcome, actionFor(b.Outcome, branchEmit(r, b))
		}
	}
	return r.Values[len(r.Values)-1].Outcome, nil
}

func actionFor(o Outcome, a *Action) *Action {
	if o.Status == kcmigrate.StatusUnsupported {
		return nil
	}
	return a
}

func classUnsupported(class string) Outcome {
	reason := "The connector class " + class + " has no Conduit mapping in this version."
	if class == "" {
		reason = "The connector config has no connector.class."
	}
	return Outcome{
		Status: kcmigrate.StatusUnsupported, Blocking: true, Code: kcmigrate.CodeConnectorClassUnsupported.Reason(),
		Reason: reason,
		Fix:    "Keep this connector on Kafka Connect. `conduit migrate kafka-connect --help` lists the classes this version migrates.",
	}
}

func unknownKey(key string, f *File) Outcome {
	where := "these rules"
	if f != nil {
		name := f.Class
		if name == "" {
			name = f.Scope
		}
		where = fmt.Sprintf("the %s rules (written against %s %s)", name, f.Upstream.Name, f.Upstream.Version)
	}
	return Outcome{
		Status: kcmigrate.StatusUnsupported, Blocking: true, Code: kcmigrate.CodeKeyUnknown.Reason(),
		Reason: fmt.Sprintf("No rule in %s knows %s.", where, key),
		Fix:    "Check the key for a typo. If it is a key of a newer upstream version, keep this connector on Kafka Connect until migrate's rules cover it.",
	}
}

func duplicate(later kcmigrate.Origin) Outcome {
	return Outcome{
		Status: kcmigrate.StatusTranslated, Code: kcmigrate.CodeKeyDuplicate.Reason(),
		Reason: "Overridden by the occurrence at " + later.String() + "; the last value wins, as in Kafka Connect.",
	}
}

// index builds the lookup structures of a validated file.
func (f *File) index() {
	f.exact = map[string]*Rule{}
	for i := range f.Keys {
		r := &f.Keys[i]
		switch {
		case r.Key.Prefix != "":
			f.prefixes = append(f.prefixes, r)
		case r.Key.Regex != "":
			f.regexes = append(f.regexes, r)
		default:
			f.exact[r.Key.Exact] = r
		}
	}
	sort.SliceStable(f.prefixes, func(i, j int) bool { return len(f.prefixes[i].Key.Prefix) > len(f.prefixes[j].Key.Prefix) })
}

// match returns the rule for key: an exact rule, else the longest matching
// prefix, else the matching regex. Nil if none.
func (f *File) match(key string) *Rule {
	if f == nil {
		return nil
	}
	if r, ok := f.exact[key]; ok {
		return r
	}
	for _, r := range f.prefixes {
		if strings.HasPrefix(key, r.Key.Prefix) {
			return r
		}
	}
	for _, r := range f.regexes {
		if r.re.MatchString(key) {
			return r
		}
	}
	return nil
}

// checkRegexOverlap fails if a probe key that neither an exact nor a prefix
// rule claims matches two regex rules, which would make the outcome depend
// on rule order. Probes are every exact key, every prefix extended by one
// character, and every regex's literal prefix extended likewise.
func (f *File) checkRegexOverlap() error {
	if len(f.regexes) < 2 {
		return nil
	}
	var probes []string
	for _, r := range f.Keys {
		switch {
		case r.Key.Prefix != "":
			probes = append(probes, r.Key.Prefix+"x")
		case r.re != nil:
			lit, _ := r.re.LiteralPrefix()
			probes = append(probes, lit, lit+"x")
		default:
			probes = append(probes, r.Key.Exact)
		}
	}
	var errs []error
	for _, p := range probes {
		if r := f.match(p); r == nil || r.re == nil {
			continue // unclaimed, or claimed by an exact or prefix rule first
		}
		var hits []string
		for _, r := range f.regexes {
			if r.re.MatchString(p) {
				hits = append(hits, r.Key.Regex)
			}
		}
		if len(hits) > 1 {
			errs = append(errs, cerrors.Errorf("%s: key %q matches more than one regex rule: %s", f.name, p, strings.Join(hits, ", ")))
		}
	}
	return cerrors.Join(errs...)
}

func branchEmit(r *Rule, b ValueBranch) *Action {
	if b.Emit != nil {
		return b.Emit
	}
	return r.Emit
}

// DefaultNotes returns the connector-level notes for the upstream defaults
// that apply to c: every default of the class file and of the shared file
// whose key c does not set (nor the worker, for a default marked worker).
// An unknown class has no notes: all its keys already block.
func (e *Engine) DefaultNotes(c kcmigrate.Connector, workerKeys map[string]bool) []kcmigrate.Note {
	f, ok := e.classes[c.Class]
	if !ok {
		return nil
	}
	set := map[string]bool{}
	for _, kv := range c.Config {
		set[kv.Key] = true
	}
	var notes []kcmigrate.Note
	for _, file := range []*File{f, e.shared} {
		if file == nil {
			continue
		}
		for _, d := range file.Defaults {
			if d.Note == nil || set[d.Key] || (d.Worker && workerKeys[d.Key]) {
				continue
			}
			notes = append(notes, kcmigrate.Note{
				Code: d.Note.Code, Reason: d.Note.Reason, Fix: d.Note.Fix, Blocking: d.Note.Blocking,
				Rule: strings.TrimSuffix(file.name, ".yaml") + "/defaults/" + d.Key,
			})
		}
	}
	return notes
}

// specificity ranks a matched rule: an exact key, then prefixes by length,
// then a regex; zero for no rule.
func specificity(r *Rule) int {
	switch {
	case r == nil:
		return 0
	case r.Key.Prefix != "":
		return 2 + len(r.Key.Prefix)
	case r.re != nil:
		return 1
	default:
		return 1 << 20
	}
}

// RulesUsed describes the rule files a run consulted: the class file of
// every class with rules, the shared file when at least one such class is
// present, and the worker file for a run with a worker. Listing only these
// keeps a report independent of rule files added for other classes.
func (e *Engine) RulesUsed(classes []string, worker bool) []kcmigrate.RulesInfo {
	used := map[*File]bool{}
	for _, c := range classes {
		if f, ok := e.classes[c]; ok {
			used[f] = true
			if e.shared != nil {
				used[e.shared] = true
			}
		}
	}
	if worker && e.worker != nil {
		used[e.worker] = true
	}
	out := []kcmigrate.RulesInfo{}
	for _, f := range e.files { // e.files is sorted
		if used[f] {
			out = append(out, f.info())
		}
	}
	return out
}
