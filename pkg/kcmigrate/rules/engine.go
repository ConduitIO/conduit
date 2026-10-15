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

// Connector classifies every key occurrence of c, in input order. A class
// with no rule file yields the role unknown and one blocking
// kc.connector_class_unsupported entry per key. Otherwise each key is looked
// up in the class's rules, then in the shared rules; a key no rule claims is
// kc.key_unknown, blocking. Notes, transforms and pipeline targets are left
// to the emitters.
func (e *Engine) Connector(c kcmigrate.Connector) kcmigrate.ReportConnector {
	rc := kcmigrate.ReportConnector{
		Name: c.Name, Class: c.Class,
		Entries: make([]kcmigrate.Entry, 0, len(c.Config)), Notes: []kcmigrate.Note{}, Transforms: []kcmigrate.Transform{},
	}
	f, ok := e.classes[c.Class]
	if !ok {
		rc.Role = kcmigrate.RoleUnknown
		for _, kv := range c.Config {
			rc.Entries = append(rc.Entries, classUnsupported(c.Class).entry(kv.Key, kv.Origin))
		}
		return rc
	}
	rc.Role = kcmigrate.Role(f.Scope)
	rc.Entries = e.classifyAll(c.Config, f, e.shared)
	return rc
}

// Worker classifies a worker file's keys with the worker rules. The report
// element's name is the file label.
func (e *Engine) Worker(w kcmigrate.Worker) kcmigrate.ReportConnector {
	return kcmigrate.ReportConnector{
		Name: w.File, Role: kcmigrate.RoleWorker,
		Entries: e.classifyAll(w.Props, e.worker, nil), Notes: []kcmigrate.Note{}, Transforms: []kcmigrate.Transform{},
	}
}

// classifyAll is where the completeness invariant is upheld: exactly one
// entry per KV, in input order, whatever the rules say.
func (e *Engine) classifyAll(kvs []kcmigrate.KV, primary, fallback *File) []kcmigrate.Entry {
	last := map[string]int{}
	for i, kv := range kvs {
		last[kv.Key] = i
	}
	out := make([]kcmigrate.Entry, 0, len(kvs))
	for i, kv := range kvs {
		if j := last[kv.Key]; j != i {
			out = append(out, duplicate(kvs[j].Origin).entry(kv.Key, kv.Origin))
			continue
		}
		out = append(out, classify(kv, primary, fallback))
	}
	return out
}

func classify(kv kcmigrate.KV, primary, fallback *File) kcmigrate.Entry {
	home := primary
	r := home.match(kv.Key)
	if r == nil && fallback != nil {
		home = fallback
		r = home.match(kv.Key)
	}
	if r == nil {
		return unknownKey(kv.Key, primary).entry(kv.Key, kv.Origin)
	}
	alias := r.AliasOf
	if alias != "" {
		r = home.exact[alias]
	}
	ent := r.outcomeFor(kv.Value).entry(kv.Key, kv.Origin)
	if alias != "" {
		legacy := fmt.Sprintf("%s is the legacy name of %s and is handled as %s", kv.Key, alias, alias)
		if ent.Status == kcmigrate.StatusTranslated {
			ent.Status, ent.Code = kcmigrate.StatusApproximated, kcmigrate.CodeKeyDeprecated.Reason()
			ent.Reason, ent.Fix = legacy+".", "Rename "+kv.Key+" to "+alias+"."
		} else {
			ent.Reason += " (" + legacy + ".)"
		}
	}
	return ent
}

// referencePattern is Kafka Connect's ConfigTransformer pattern for
// config-provider references such as ${file:/path:key} and ${env:NAME}.
var referencePattern = regexp.MustCompile(`\$\{([^}]*?):(([^}]*?):)?([^}]*?)\}`)

func (r *Rule) outcomeFor(v kcmigrate.Value) Outcome {
	if len(r.Values) == 0 {
		return r.Outcome
	}
	// Compared only, never copied into the outcome. Trimmed as Kafka
	// Connect's ConfigDef.parseType trims every string-typed value.
	raw := strings.TrimSpace(v.Reveal())
	if referencePattern.MatchString(raw) {
		return Outcome{
			Status: kcmigrate.StatusUnsupported, Blocking: true, Code: kcmigrate.CodeKeyUnsupported.Reason(),
			Reason: "The value is a config-provider reference. How this key is migrated depends on its value, which migrate cannot read offline.",
			Fix:    "Set the literal value in the Kafka Connect config, or resolve the reference and re-run.",
		}
	}
	for _, b := range r.Values[:len(r.Values)-1] {
		for _, m := range b.Match {
			if m == raw || (r.IgnoreCase && strings.EqualFold(m, raw)) {
				return b.Outcome
			}
		}
	}
	return r.Values[len(r.Values)-1].Outcome
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
