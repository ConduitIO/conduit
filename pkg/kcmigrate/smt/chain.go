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

package smt

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
)

// Keys of the chain.
const (
	keyTransforms     = "transforms"
	keyPredicates     = "predicates"
	prefixTransforms  = keyTransforms + "."
	prefixPredicates  = keyPredicates + "."
	optType           = "type"
	optPredicate      = "predicate"
	optNegate         = "negate"
	processorIDPrefix = "smt-"
)

// Env is the connector a chain belongs to.
type Env struct {
	// Scope is rules.ScopeSource or rules.ScopeSink.
	Scope string
	// Source and Destination are the pipeline's plugins.
	Source, Destination string
	// Topic is the topic setting the connector rules wrote, as a template
	// (see TopicPipeline); empty when they left it unset.
	Topic string
	// ProcessorPath is the JSON pointer of the processors list the chain's
	// processors are appended to, and ProcessorOffset the number of
	// processors already on it.
	ProcessorPath   string
	ProcessorOffset int
	// SchemaMode is true when Kafka Connect runs the chain in schema mode:
	// on a Debezium source chain, over the Debezium Struct whatever
	// converter follows. On a sink chain the converter decides; only
	// ByteArrayConverter is translated, so it is false there.
	SchemaMode bool
	// KeyShape and ValueShape are what the record sides hold where the
	// chain starts, as Kafka Connect's SMTs see them: unknown unless the
	// connector and converter make it certain.
	KeyShape, ValueShape Shape
	// KeySchema and ValueSchema are true when the side's effective Kafka
	// Connect converter carries a schema (Avro, Protobuf, JSON Schema,
	// JsonConverter with schemas.enable=true), and also when the converter
	// is unknown, so the check fails closed. Decision 8 refuses an SMT that
	// reshapes such a side: Kafka Connect derives a type-preserving schema
	// for the new shape, and Conduit would register one with widened types.
	KeySchema, ValueSchema bool
	// DebeziumOutput is true when the pipeline writes Debezium-compatible
	// output (decision 5: SMTs that reshape the envelope are refused then).
	DebeziumOutput bool
}

// Shape is what a record side is known to hold.
type Shape string

const (
	// ShapeUnknown: a translator that reshapes the side must refuse.
	ShapeUnknown Shape = ""
	// ShapeStructured: StructuredData, after a native CDC source.
	ShapeStructured Shape = "structured"
	// ShapeRaw: raw bytes, from the Kafka source.
	ShapeRaw Shape = "raw"
)

// Outcome is the result of a chain: the processors to append, in order, and
// the report's transform index. The decisions passed to Apply carry the
// entries.
type Outcome struct {
	Processors []Emitted
	Transforms []kcmigrate.Transform
	// Topic is the topic template the chain ends with when a translator
	// changed it; TopicChanged says whether to write it.
	Topic        string
	TopicChanged bool
	// Consulted is true when the connector has a transform chain, so the
	// smt data was read.
	Consulted bool
}

// Emitted is one processor of a translated chain.
type Emitted struct {
	ID        string
	Plugin    string
	Condition string // the condition setting (a template), empty for none
	Settings  map[string]string
	// Path is the processor's JSON pointer once appended.
	Path string
}

// Apply decides every decision whose emit kind is smt, in place: it
// groups the transforms and predicates keys per alias in chain order,
// translates each SMT with reg, and either writes processors for the whole
// chain or none (a refused link refuses the chain, design "All or nothing
// per chain"). Every claimed key gets an outcome; a key left undecided is
// an error, never a dropped entry. Decisions of other kinds are untouched.
func Apply(data *Data, reg *Registry, env Env, ds []rules.Decision) (Outcome, error) {
	a := &applier{data: data, reg: reg, env: env, ds: ds, decided: map[int]bool{}, preds: map[string]*predState{}}
	a.out.Transforms = []kcmigrate.Transform{} // never null in the --json result
	if err := a.run(); err != nil {
		return Outcome{}, err
	}
	for i := range ds {
		if isChainKey(ds[i]) && !a.decided[i] {
			return Outcome{}, cerrors.Errorf("SMT key %s was claimed by the chain translator but given no outcome", ds[i].KV.Key)
		}
	}
	return a.out, nil
}

// IsChainKey reports whether the decision is claimed by the chain
// translator.
func isChainKey(d rules.Decision) bool {
	return d.Emit != nil && d.Emit.Kind == rules.SMTKind
}

type applier struct {
	data    *Data
	reg     *Registry
	env     Env
	ds      []rules.Decision
	decided map[int]bool
	out     Outcome

	transforms, predicates int // decision index of the list keys, -1 if absent
	aliases, predAliases   []string
	links                  []*link
	preds                  map[string]*predState
	predOrder              []string
	chainErrs              []string // reasons that refuse the chain as a whole
	predErrs               []string // the subset about the predicates list
	predsRead              bool     // false when predicates= is a config-provider reference
	nested                 map[string][]string
	deciding               bool // set once the chain's verdict is taken
	chain                  Chain
}

// link is one alias of transforms=.
type link struct {
	alias     string
	keys      []int // every decision of the alias, in input order
	typeKey   int   // -1 if absent
	predKey   int
	negateKey int
	options   []int
	smt       *SMT
	refusal   *Refusal
	refusedAt int   // decision the refusal is reported on; -1 for the transforms key
	unknown   []int // options the translator did not claim
	procs     []Processor
	reason    string
	reads     []Path
	writes    []Path
	emitted   []int // indexes into a.out.Processors
}

type predState struct {
	alias     string
	keys      []int
	typeKey   int
	options   []int
	pred      *Predicate
	cond      string
	reads     []Path
	refusal   *Refusal
	refusedAt int
	unknown   []int
	used      []string // aliases of the transforms that name it, in chain order
	done      bool
}

func (a *applier) run() error {
	a.transforms, a.predicates = -1, -1
	var tkeys, pkeys []int
	for i, d := range a.ds {
		if !isChainKey(d) {
			continue
		}
		switch k := d.KV.Key; {
		case k == keyTransforms:
			a.transforms = i
		case k == keyPredicates:
			a.predicates = i
		case strings.HasPrefix(k, prefixTransforms):
			tkeys = append(tkeys, i)
		case strings.HasPrefix(k, prefixPredicates):
			pkeys = append(pkeys, i)
		default:
			return cerrors.Errorf("key %s is claimed by %s but is not a transforms or predicates key", k, rules.SMTKind)
		}
	}
	if a.transforms < 0 && a.predicates < 0 && len(tkeys) == 0 && len(pkeys) == 0 {
		return nil
	}
	topic, known := TopicPipeline(a.env.Topic)
	a.chain = Chain{env: a.env, data: a.data, topic: topic, known: known}

	tRead := a.parseList(a.transforms, keyTransforms, &a.aliases, &a.chainErrs)
	a.predsRead = a.parseList(a.predicates, keyPredicates, &a.predAliases, &a.predErrs)
	a.out.Consulted = len(a.aliases)+len(a.predAliases)+len(tkeys)+len(pkeys)+len(a.chainErrs)+len(a.predErrs) > 0
	a.groupPredicates(pkeys, a.predsRead)
	a.groupTransforms(tkeys, tRead)
	a.recordPredicateUsers()
	a.unusedPredicateReferences()
	for _, l := range a.links {
		if err := a.translate(l); err != nil {
			return err
		}
	}
	a.checkLaterWrites()
	a.chainErrs = append(a.chainErrs, a.predErrs...)
	refused := a.refusedAliases()
	if len(a.chainErrs) == 0 && len(refused) == 0 {
		a.emit()
	}
	a.decide(refused)
	a.index()
	return nil
}

// listSplit is Kafka Connect's LIST split, \s*,\s* with Java's \s, which
// includes \x0B where Go's does not.
var listSplit = regexp.MustCompile(`[ \t\n\x0B\f\r]*,[ \t\n\x0B\f\r]*`)

// javaTrim is Java's String.trim: it strips the characters up to U+0020 at
// both ends, and keeps U+0085 and U+00A0, which strings.TrimSpace strips.
// Kafka Connect trims LIST, CLASS, STRING and BOOLEAN values this way.
func javaTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r <= ' ' })
}

// parseList reads transforms= or predicates= as Kafka Connect's LIST type
// does (trimmed, split on commas with surrounding whitespace; an empty value
// is an empty list). It returns false when the value is a config-provider
// reference, whose aliases cannot be known offline.
func (a *applier) parseList(idx int, name string, into *[]string, errs *[]string) bool {
	if idx < 0 {
		return true
	}
	raw := javaTrim(a.ds[idx].KV.Value.Reveal()) // parsed, never copied into the report
	if rules.IsReference(raw) {
		*errs = append(*errs, name+" is a config-provider reference, so which "+aliasNoun(name)+" it lists cannot be read offline")
		return false
	}
	if raw == "" {
		return true
	}
	seen := map[string]bool{}
	for _, alias := range listSplit.Split(raw, -1) {
		if seen[alias] {
			*errs = append(*errs, fmt.Sprintf("%s lists %q twice, and Kafka Connect rejects duplicate aliases", name, alias))
			continue
		}
		seen[alias] = true
		*into = append(*into, alias)
	}
	return true
}

func aliasNoun(list string) string {
	if list == keyPredicates {
		return "predicates"
	}
	return "transforms"
}

// owner returns the listed alias a key belongs to: the longest alias whose
// prefix the key has, so alias a.b owns transforms.a.b.type, not alias a.
func owner(rest string, aliases []string) (string, string, bool) {
	best := -1
	for i, al := range aliases {
		if strings.HasPrefix(rest, al+".") && (best < 0 || len(al) > len(aliases[best])) {
			best = i
		}
	}
	if best < 0 {
		return "", "", false
	}
	return aliases[best], rest[len(aliases[best])+1:], true
}

func (a *applier) groupPredicates(pkeys []int, listRead bool) {
	for _, al := range a.predAliases {
		a.preds[al] = &predState{alias: al, typeKey: -1, refusedAt: -1}
		a.predOrder = append(a.predOrder, al)
	}
	for _, i := range pkeys {
		key := a.ds[i].KV.Key
		if !listRead {
			a.refuse(i, kcmigrate.CodeSMTUnsupported.Reason(),
				"predicates is a config-provider reference, so migrate cannot tell which predicate "+key+" belongs to.",
				"Set predicates to the literal list of aliases and re-run.")
			continue
		}
		al, opt, ok := owner(strings.TrimPrefix(key, prefixPredicates), a.predAliases)
		if !ok {
			a.unlisted(i, "Kafka Connect ignores predicate settings for an alias predicates does not list.")
			continue
		}
		p := a.preds[al]
		p.keys = append(p.keys, i)
		if opt == optType {
			p.typeKey = i
		} else {
			p.options = append(p.options, i)
		}
	}
	nested := nestedAliases(a.predAliases)
	for _, al := range a.predOrder {
		p := a.preds[al]
		if p.typeKey < 0 {
			a.predErrs = append(a.predErrs, fmt.Sprintf("predicates lists %q but predicates.%s.type is not set, and Kafka Connect rejects the config", al, al))
		}
		if others := nested[al]; len(others) > 0 {
			why := nestedReason(keyPredicates, al, others)
			a.predErrs = append(a.predErrs, strings.TrimSuffix(why, "."))
			p.done, p.refusedAt = true, p.typeKey
			p.refusal = &Refusal{Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: why, Fix: "Rename the predicates so that no alias is a dotted prefix of another, then re-run."}
		}
	}
}

// nestedAliases returns, per alias, the other listed aliases it is a dotted
// prefix of or that are a dotted prefix of it. Kafka Connect configures
// alias a with every key under transforms.a., so with a and a.b listed it
// hands transforms.a.b.* to both: as a's options b.* and as a.b's own keys.
// One key reported under one alias would hide that the other SMT reads it
// too, so both are refused.
func nestedAliases(aliases []string) map[string][]string {
	out := map[string][]string{}
	for _, x := range aliases {
		for _, y := range aliases {
			if x != y && strings.HasPrefix(y, x+".") {
				out[x] = append(out[x], y)
				out[y] = append(out[y], x)
			}
		}
	}
	return out
}

func nestedReason(list, alias string, others []string) string {
	return fmt.Sprintf("%s lists %s together with %s; Kafka Connect hands the settings under the longer alias to both, as options of the shorter one too, so the SMTs share settings and migrate does not translate them.",
		list, alias, strings.Join(others, ", "))
}

// recordPredicateUsers notes, before any refusal, which transforms name each
// predicate, in chain order, so a predicate whose only users are refused is
// reported as used by them, never as unused.
func (a *applier) recordPredicateUsers() {
	for _, l := range a.links {
		if l.predKey < 0 {
			continue
		}
		if p, ok := a.preds[javaTrim(a.ds[l.predKey].KV.Value.Reveal())]; ok {
			p.used = append(p.used, l.alias)
		}
	}
}

// unusedPredicateReferences refuses the chain for a config-provider
// reference in a predicate no transform names (see unlisted), before the
// chain's verdict; the keys themselves are decided with the predicates.
func (a *applier) unusedPredicateReferences() {
	for _, al := range a.predOrder {
		p := a.preds[al]
		if len(p.used) > 0 {
			continue
		}
		for _, i := range p.keys {
			if rules.IsReference(a.ds[i].KV.Value.Reveal()) {
				a.chainErrs = append(a.chainErrs, a.ds[i].KV.Key+" is a config-provider reference migrate cannot read offline")
			}
		}
	}
}

func (a *applier) groupTransforms(tkeys []int, listRead bool) {
	a.nested = nestedAliases(a.aliases)
	byAlias := map[string]*link{}
	for _, al := range a.aliases {
		l := &link{alias: al, typeKey: -1, predKey: -1, negateKey: -1, refusedAt: -1}
		byAlias[al] = l
		a.links = append(a.links, l)
	}
	for _, i := range tkeys {
		key := a.ds[i].KV.Key
		if !listRead {
			a.refuse(i, kcmigrate.CodeSMTUnsupported.Reason(),
				"transforms is a config-provider reference, so migrate cannot tell whether "+key+" belongs to a transform Kafka Connect runs.",
				"Set transforms to the literal list of aliases and re-run.")
			continue
		}
		al, opt, ok := owner(strings.TrimPrefix(key, prefixTransforms), a.aliases)
		if !ok {
			a.unlisted(i, "Kafka Connect ignores transform settings for an alias transforms does not list.")
			continue
		}
		l := byAlias[al]
		l.keys = append(l.keys, i)
		switch opt {
		case optType:
			l.typeKey = i
		case optPredicate:
			l.predKey = i
		case optNegate:
			l.negateKey = i
		default:
			l.options = append(l.options, i)
		}
	}
}

// refuseLink refuses l with reason, reported on decision at (-1: the
// transforms key).
func (l *link) refuse(r Refusal, at int) {
	if l.refusal == nil {
		l.refusal, l.refusedAt = &r, at
	}
}

// prepare builds the SMT a translator sees and refuses what the chain
// itself decides: no or empty type, a config-provider reference, a bad
// negate, an unusable predicate. It returns nil when l is refused.
func (a *applier) prepare(l *link) (*SMT, error) {
	if others := a.nested[l.alias]; len(others) > 0 {
		l.refuse(Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: nestedReason(keyTransforms, l.alias, others),
			Fix: "Rename the aliases so that no alias is a dotted prefix of another, then re-run.",
		}, l.typeKey)
		return nil, nil
	}
	if l.typeKey < 0 {
		l.refuse(Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: fmt.Sprintf("transforms lists %q but transforms.%s.type is not set, and Kafka Connect rejects the config.", l.alias, l.alias),
			Fix:    fmt.Sprintf("Set transforms.%s.type or remove %s from transforms, then re-run.", l.alias, l.alias),
		}, -1)
		return nil, nil
	}
	for _, i := range l.keys {
		if rules.IsReference(a.ds[i].KV.Value.Reveal()) {
			l.refuse(referenceRefusal(), i)
			return nil, nil
		}
	}
	class := javaTrim(a.ds[l.typeKey].KV.Value.Reveal())
	base, side := splitClass(class)
	t := &SMT{Alias: l.alias, Class: class, Base: base, Side: side}
	l.smt = t
	for _, i := range l.options {
		t.options = append(t.options, a.option(i, prefixTransforms+l.alias+"."))
	}
	if class == "" {
		l.refuse(Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: "transforms." + l.alias + ".type is empty, and Kafka Connect rejects the config.",
			Fix: "Set the transform class, then re-run.",
		}, l.typeKey)
		return nil, nil
	}
	if l.negateKey >= 0 {
		switch v := strings.ToLower(javaTrim(a.ds[l.negateKey].KV.Value.Reveal())); v {
		case "true", "false":
			t.Negate = v == "true"
		default:
			l.refuse(Refusal{
				Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: "negate is not true or false, and Kafka Connect rejects the config.",
				Fix: "Set negate to true or false, then re-run.",
			}, l.negateKey)
			return nil, nil
		}
	}
	if l.predKey >= 0 {
		r, err := a.usePredicate(l)
		if err != nil {
			return nil, err
		}
		if r != nil {
			l.refuse(*r, l.predKey)
			return nil, nil
		}
	}
	return t, nil
}

func (a *applier) translate(l *link) error {
	t, err := a.prepare(l)
	if t == nil || err != nil {
		return err
	}
	base, class := t.Base, t.Class
	tr, ok := a.reg.Transforms[base]
	if !ok {
		l.refuse(a.data.refusal(base), l.typeKey)
		return nil
	}
	c := a.chain // a translator's topic change takes effect only if it is not refused
	res, err := tr(&c, t)
	if err != nil {
		return cerrors.Errorf("transform %s (%s): %w", l.alias, class, err)
	}
	if err := a.checkResult(l, res); err != nil {
		return err
	}
	if res.Refusal != nil {
		at := l.typeKey
		if res.Refusal.Option != "" {
			at = a.optionKey(l.options, prefixTransforms+l.alias+".", res.Refusal.Option)
		}
		l.refuse(*res.Refusal, at)
		return nil
	}
	l.unknown = unclaimed(a.ds, l.options, prefixTransforms+l.alias+".", res.Claimed)
	if len(l.unknown) > 0 {
		l.refuse(Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "it has settings the " + base + " translator does not know: " + keysOf(a.ds, l.unknown) + ".",
			Fix:    "Remove the unknown settings if Kafka Connect ignores them, or keep this connector on Kafka Connect.",
		}, l.unknown[0])
		return nil
	}
	if res.Unwraps && a.chain.unwrapped {
		l.refuse(Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "The chain already unwrapped the Debezium envelope earlier, and a second unwrap point is not translated.",
			Fix:    "Remove the second unwrap, or keep this connector on Kafka Connect.",
		}, l.typeKey)
		return nil
	}
	c.unwrapped = c.unwrapped || res.Unwraps
	if len(res.Processors) == 0 && t.Predicate != "" && !res.UsesPredicate {
		// The predicate would become the condition of the SMT's processors,
		// and there are none: a predicated no-op or a topic rewrite cannot
		// carry it (design, "Predicates and negate").
		l.refuse(Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "The transform writes no processor, so its predicate has no processor condition to become.",
			Fix:    "Remove the predicate if the transform should apply to every record, or keep this connector on Kafka Connect.",
		}, l.predKey)
		return nil
	}
	l.procs, l.reason, l.reads, l.writes = res.Processors, res.Reason, res.Reads, res.Writes
	if res.UsesPredicate {
		l.reads = append(l.reads, a.preds[t.Predicate].reads...)
	}
	a.chain = c
	return nil
}

// checkLaterWrites refuses an SMT whose logic is evaluated against the
// final record (a topic expression) when a later SMT of the chain writes a
// path it reads: Kafka Connect evaluated it at its own position.
func (a *applier) checkLaterWrites() {
	for i, l := range a.links {
		if l.refusal != nil {
			continue
		}
		for _, later := range a.links[i+1:] {
			for _, r := range l.reads {
				for _, w := range later.writes {
					if r.overlaps(w) {
						l.refuse(Refusal{
							Code:   kcmigrate.CodeSMTUnsupported.Reason(),
							Reason: fmt.Sprintf("The transform %s later in the chain writes %s, which this transform reads; its result is evaluated against the final record, so it would read the changed value where Kafka Connect read the original.", later.alias, w),
							Fix:    "Reorder the transforms so that nothing after this one writes what it reads, or keep this connector on Kafka Connect.",
						}, l.typeKey)
					}
				}
			}
		}
	}
}

// unlisted decides a key Kafka Connect does not apply as a no-op, unless
// its value is a config-provider reference: Kafka Connect resolves those for
// every key and fails the connector when it cannot, which migrate cannot
// check offline, so it blocks (design, "Grouping and per-key outcomes").
func (a *applier) unlisted(i int, reason string) {
	if rules.IsReference(a.ds[i].KV.Value.Reveal()) {
		r := referenceRefusal()
		a.refuse(i, r.Code, r.Reason+" Kafka Connect resolves it even where the setting is not used, and fails the connector when it cannot.", r.Fix)
		// The connector may not start at all, so no part of the chain is
		// known to run as written: the chain is refused with it.
		if !a.deciding {
			a.chainErrs = append(a.chainErrs, a.ds[i].KV.Key+" is a config-provider reference migrate cannot read offline")
		}
		return
	}
	a.translated(i, reason, "")
}

func referenceRefusal() Refusal {
	return Refusal{
		Code:   kcmigrate.CodeSMTUnsupported.Reason(),
		Reason: "The value is a config-provider reference. A transform's behavior depends on its settings, which migrate cannot read offline.",
		Fix:    "Set the literal value in the Kafka Connect config, or resolve the reference and re-run.",
	}
}

// checkResult rejects a result a translator must never return: a bug.
func (a *applier) checkResult(l *link, res Result) error {
	names := map[string]bool{}
	for _, i := range l.options {
		names[strings.TrimPrefix(a.ds[i].KV.Key, prefixTransforms+l.alias+".")] = true
	}
	switch {
	case res.Refusal != nil && len(res.Processors) > 0:
		return cerrors.Errorf("transform %s: a refusal with processors", l.alias)
	case res.Refusal != nil:
		if err := res.Refusal.validate(); err != nil {
			return cerrors.Errorf("transform %s: %w", l.alias, err)
		}
		if res.Refusal.Option != "" && !names[res.Refusal.Option] {
			return cerrors.Errorf("transform %s: refusal names option %q, which it does not have", l.alias, res.Refusal.Option)
		}
	case len(res.Processors) == 0 && res.Reason == "":
		return cerrors.Errorf("transform %s: no processor and no reason", l.alias)
	}
	for _, c := range res.Claimed {
		if !names[c] {
			return cerrors.Errorf("transform %s: claims option %q, which it does not have", l.alias, c)
		}
	}
	for _, p := range res.Processors {
		if p.Plugin == "" {
			return cerrors.Errorf("transform %s: a processor without a plugin", l.alias)
		}
		for _, f := range p.From {
			if !names[f] {
				return cerrors.Errorf("transform %s: processor from option %q, which it does not have", l.alias, f)
			}
		}
	}
	return nil
}

func (a *applier) option(i int, prefix string) Option {
	kv := a.ds[i].KV
	return Option{Name: strings.TrimPrefix(kv.Key, prefix), Key: kv.Key, value: kv.Value}
}

func (a *applier) optionKey(opts []int, prefix, name string) int {
	at := -1
	for _, i := range opts {
		if strings.TrimPrefix(a.ds[i].KV.Key, prefix) == name {
			at = i
		}
	}
	return at
}

func unclaimed(ds []rules.Decision, opts []int, prefix string, claimed []string) []int {
	var out []int
	for _, i := range opts {
		if !slices.Contains(claimed, strings.TrimPrefix(ds[i].KV.Key, prefix)) {
			out = append(out, i)
		}
	}
	return out
}

func keysOf(ds []rules.Decision, idx []int) string {
	var ks []string
	for _, i := range idx {
		ks = append(ks, ds[i].KV.Key)
	}
	return strings.Join(ks, ", ")
}

// usePredicate translates the predicate l names (once per predicate) and
// returns a refusal for l if it cannot be used.
func (a *applier) usePredicate(l *link) (*Refusal, error) {
	name := javaTrim(a.ds[l.predKey].KV.Value.Reveal())
	l.smt.Predicate = name
	p, ok := a.preds[name]
	if !ok && !a.predsRead {
		return &Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "predicates is a config-provider reference, so the predicate the transform names cannot be read offline.",
			Fix:    "Set predicates to the literal list of aliases and re-run.",
		}, nil
	}
	if !ok {
		return &Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: fmt.Sprintf("The transform names the predicate %q, which predicates does not list, and Kafka Connect rejects the config.", name),
			Fix:    "Define the predicate in predicates, or remove transforms." + l.alias + ".predicate, then re-run.",
		}, nil
	}
	if !p.done {
		p.done = true
		if err := a.translatePredicate(p); err != nil {
			return nil, err
		}
	}
	if p.refusal != nil {
		return &Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: fmt.Sprintf("Its predicate %q is not translated. %s", name, p.refusal.Reason),
			Fix:    p.refusal.Fix,
		}, nil
	}
	l.smt.PredicateCondition = p.cond
	if l.smt.Negate {
		l.smt.PredicateCondition = Not(p.cond)
	}
	return nil, nil
}

func (a *applier) translatePredicate(p *predState) error {
	if p.typeKey < 0 {
		p.refusal = &Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: "predicates." + p.alias + ".type is not set.",
			Fix: "Set the predicate class, then re-run.",
		}
		return nil
	}
	for _, i := range p.keys {
		if rules.IsReference(a.ds[i].KV.Value.Reveal()) {
			r := referenceRefusal()
			p.refusal, p.refusedAt = &r, i
			return nil
		}
	}
	class := javaTrim(a.ds[p.typeKey].KV.Value.Reveal())
	base, _ := splitClass(class)
	p.pred = &Predicate{Alias: p.alias, Class: class, Base: base}
	prefix := prefixPredicates + p.alias + "."
	for _, i := range p.options {
		p.pred.options = append(p.pred.options, a.option(i, prefix))
	}
	tr, ok := a.reg.Predicates[base]
	if !ok {
		r := a.data.refusal(base)
		p.refusal, p.refusedAt = &r, p.typeKey
		return nil
	}
	c := a.chain
	res, err := tr(&c, p.pred)
	switch {
	case err != nil:
		return cerrors.Errorf("predicate %s (%s): %w", p.alias, class, err)
	case res.Refusal != nil:
		if err := res.Refusal.validate(); err != nil {
			return cerrors.Errorf("predicate %s: %w", p.alias, err)
		}
		r := *res.Refusal
		p.refusal, p.refusedAt = &r, p.typeKey
		if r.Option != "" {
			if at := a.optionKey(p.options, prefix, r.Option); at >= 0 {
				p.refusedAt = at
			}
		}
	case res.Condition == "":
		return cerrors.Errorf("predicate %s: the %s translator returned no condition and no refusal", p.alias, base)
	default:
		p.unknown = unclaimed(a.ds, p.options, prefix, res.Claimed)
		if len(p.unknown) > 0 {
			p.refusal = &Refusal{
				Code:   kcmigrate.CodeSMTUnsupported.Reason(),
				Reason: "it has settings the " + base + " translator does not know: " + keysOf(a.ds, p.unknown) + ".",
				Fix:    "Remove the unknown settings if Kafka Connect ignores them, or keep this connector on Kafka Connect.",
			}
			p.refusedAt = p.unknown[0]
			return nil
		}
		p.cond, p.reads = res.Condition, res.Reads
	}
	return nil
}

func (a *applier) refusedAliases() []string {
	var out []string
	for _, l := range a.links {
		if l.refusal != nil {
			out = append(out, l.alias)
		}
	}
	return out
}

var idUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// emit writes the processors of a translated chain: IDs smt-<alias>
// (sanitized), -<n> for each processor of an SMT that emits several, and a
// numeric suffix for a collision, in chain order. Conditions AND the
// predicate (negated if asked) with the translator's own condition.
func (a *applier) emit() {
	used := map[string]bool{}
	uniq := func(id string) string {
		for base, n := id, 2; used[id]; n++ {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		used[id] = true
		return id
	}
	excluded := map[Side]map[string]bool{} // side -> conditions a schema exclude already ran under
	for _, l := range a.links {
		var pred string
		if l.smt.Predicate != "" {
			pred = a.preds[l.smt.Predicate].cond
			if l.smt.Negate {
				pred = Not(pred)
			}
		}
		for j, p := range l.procs {
			id := processorIDPrefix + idUnsafe.ReplaceAllString(l.alias, "_")
			if len(l.procs) > 1 {
				id += fmt.Sprintf("-%d", j+1)
			}
			cond := Template(And(pred, p.Condition))
			if p.Reshapes != SideNone && !excluded[p.Reshapes][cond] && !excluded[p.Reshapes][""] {
				if excluded[p.Reshapes] == nil {
					excluded[p.Reshapes] = map[string]bool{}
				}
				excluded[p.Reshapes][cond] = true
				a.append(Emitted{
					ID: uniq(processorIDPrefix + "schema-" + string(p.Reshapes)), Plugin: "field.exclude",
					Condition: cond, Settings: map[string]string{"fields": schemaFields(p.Reshapes)},
				})
			}
			l.emitted = append(l.emitted, a.append(Emitted{ID: uniq(id), Plugin: p.Plugin, Condition: cond, Settings: p.Settings}))
		}
	}
	if topic, known := a.chain.Topic(); known {
		if start, _ := TopicPipeline(a.env.Topic); topic != start {
			a.out.Topic, a.out.TopicChanged = Template(topic), true
		}
	}
}

// append adds a processor at the end of the chain's processors and returns
// its index.
func (a *applier) append(e Emitted) int {
	n := len(a.out.Processors)
	e.Path = fmt.Sprintf("%s/%d", a.env.ProcessorPath, a.env.ProcessorOffset+n)
	a.out.Processors = append(a.out.Processors, e)
	return n
}

// schemaFields are the metadata references of a side's schema subject and
// version, for field.exclude.
func schemaFields(s Side) string {
	subject, version := opencdc.MetadataPayloadSchemaSubject, opencdc.MetadataPayloadSchemaVersion
	if s == SideKey {
		subject, version = opencdc.MetadataKeySchemaSubject, opencdc.MetadataKeySchemaVersion
	}
	return `.Metadata["` + subject + `"],.Metadata["` + version + `"]`
}

func (a *applier) decide(refused []string) {
	a.deciding = true
	ok := len(a.chainErrs) == 0 && len(refused) == 0
	chainWhy := a.chainReason()
	chainFix := "Make every transform of the chain translatable (see the entries of each), or keep this connector on Kafka Connect. With --accept-gaps the pipeline runs without any of the chain's transforms."
	for _, l := range a.links {
		a.decideLink(l, ok, refused, chainFix)
	}
	for _, al := range a.predOrder {
		a.decidePredicate(a.preds[al], ok, chainFix)
	}
	if a.transforms >= 0 {
		switch {
		case len(a.aliases) == 0 && len(a.chainErrs) == 0:
			a.translated(a.transforms, "No transforms.", "")
		case ok:
			a.translated(a.transforms, a.chainTranslatedReason(), a.firstPath())
		default:
			a.refuse(a.transforms, kcmigrate.CodeSMTUnsupported.Reason(), chainWhy, chainFix)
		}
	}
	if a.predicates >= 0 {
		anyUsed := false
		for _, p := range a.preds {
			anyUsed = anyUsed || len(p.used) > 0
		}
		switch {
		case len(a.predErrs) > 0:
			a.refuse(a.predicates, kcmigrate.CodeSMTUnsupported.Reason(),
				"The predicates are not translated: "+strings.Join(a.predErrs, "; ")+".", chainFix)
		case len(a.predAliases) == 0:
			a.translated(a.predicates, "No predicates.", "")
		case anyUsed && !ok:
			a.refuse(a.predicates, kcmigrate.CodeSMTUnsupported.Reason(),
				"The predicates are used by transforms of a chain that is not translated; the transforms entry says why.", chainFix)
		default:
			a.translated(a.predicates, "Each predicate becomes the condition of the processors of the transforms that name it.", "")
		}
	}
}

func (a *applier) chainReason() string {
	why := append([]string(nil), a.chainErrs...)
	for _, l := range a.links {
		if l.refusal != nil {
			why = append(why, "transform "+l.alias+": "+strings.TrimSuffix(l.refusal.Reason, "."))
		}
	}
	return "The transform chain is not translated, so no processor of it is written: " + strings.Join(why, "; ") + "."
}

func (a *applier) chainTranslatedReason() string {
	n := len(a.out.Processors)
	side := "source"
	if a.env.Scope == rules.ScopeSink {
		side = "destination"
	}
	return fmt.Sprintf("The chain %s is translated to %d processor(s) on the %s connector, in transforms order.", strings.Join(a.aliases, ", "), n, side)
}

func (a *applier) firstPath() string {
	if len(a.out.Processors) == 0 {
		return ""
	}
	return a.out.Processors[0].Path
}

func (a *applier) decideLink(l *link, ok bool, refused []string, chainFix string) {
	if ok {
		a.decideTranslatedLink(l)
		return
	}
	if l.refusal == nil {
		others := refused
		why := "the chain is not translated: " + strings.Join(a.chainErrs, "; ")
		if len(others) > 0 {
			why = "the transform " + strings.Join(others, ", ") + " in the same chain is not, and a partial chain would hand the later transforms different records than Kafka Connect does"
		}
		for _, i := range l.keys {
			a.refuse(i, kcmigrate.CodeSMTUnsupported.Reason(),
				"The transform "+l.alias+" could be translated on its own, but "+why+", so no processor of the chain is written.", chainFix)
		}
		return
	}
	r := *l.refusal
	unknown := map[int]bool{}
	for _, i := range l.unknown {
		unknown[i] = true
	}
	for _, i := range l.keys {
		switch {
		case unknown[i]:
			a.refuse(i, kcmigrate.CodeKeyUnknown.Reason(),
				fmt.Sprintf("The %s translator does not know %s; an option of a newer Kafka Connect release may change what the transform does.", l.smt.Base, a.ds[i].KV.Key),
				"Check the key for a typo, or keep this connector on Kafka Connect until migrate's translator covers it.")
		case i == l.refusedAt:
			a.refuse(i, r.Code, r.Reason, r.Fix)
		default:
			class := ""
			if l.smt != nil && l.smt.Class != "" {
				class = " (" + l.smt.Class + ")"
			}
			a.refuse(i, r.Code, "Part of the transform "+l.alias+class+", which is not translated. "+r.Reason, r.Fix)
		}
	}
}

// decideTranslatedLink gives every key of a link of a translated chain its
// outcome and the processor it became.
func (a *applier) decideTranslatedLink(l *link) {
	first := ""
	if len(l.emitted) > 0 {
		first = a.out.Processors[l.emitted[0]].Path
	}
	why := l.reason
	if len(l.emitted) > 0 {
		e := a.out.Processors[l.emitted[0]]
		why = fmt.Sprintf("Translated to the %s processor %s.", e.Plugin, e.ID)
		if len(l.emitted) > 1 {
			why = fmt.Sprintf("Translated to %d processors, the first %s (%s).", len(l.emitted), e.ID, e.Plugin)
		}
	}
	for _, i := range l.keys {
		path := first
		if name := strings.TrimPrefix(a.ds[i].KV.Key, prefixTransforms+l.alias+"."); name != "" {
			for j, p := range l.procs {
				if slices.Contains(p.From, name) {
					path = a.out.Processors[l.emitted[j]].Path
					break
				}
			}
		}
		reason := why
		switch i {
		case l.predKey:
			reason = "Becomes the condition of the transform's processors."
		case l.negateKey:
			if l.predKey < 0 {
				reason, path = "Kafka Connect ignores negate when the transform has no predicate.", ""
			} else {
				reason = "Negates the predicate in the condition of the transform's processors."
			}
		}
		a.translated(i, reason, path)
	}
}

func (a *applier) decidePredicate(p *predState, ok bool, chainFix string) {
	if len(p.used) == 0 {
		for _, i := range p.keys {
			if p.refusal != nil {
				a.refuse(i, p.refusal.Code, p.refusal.Reason, p.refusal.Fix)
				continue
			}
			if p.typeKey < 0 {
				a.refuse(i, kcmigrate.CodeSMTUnsupported.Reason(),
					"predicates lists "+p.alias+" without a type, and Kafka Connect rejects the config.", "Set predicates."+p.alias+".type or remove "+p.alias+" from predicates, then re-run.")
				continue
			}
			a.unlisted(i, "No transform names the predicate "+p.alias+", and Kafka Connect evaluates a predicate only for the transforms that name it.")
		}
		return
	}
	if ok {
		path := ""
		for _, l := range a.links {
			if l.smt != nil && l.smt.Predicate == p.alias && len(l.emitted) > 0 {
				path = a.out.Processors[l.emitted[0]].Path
				break
			}
		}
		for _, i := range p.keys {
			a.translated(i, "Becomes part of the condition of the processors of "+strings.Join(p.used, ", ")+".", path)
		}
		return
	}
	if p.refusal == nil {
		for _, i := range p.keys {
			a.refuse(i, kcmigrate.CodeSMTUnsupported.Reason(),
				"The predicate is used by "+strings.Join(p.used, ", ")+", whose chain is not translated.", chainFix)
		}
		return
	}
	unknown := map[int]bool{}
	for _, i := range p.unknown {
		unknown[i] = true
	}
	for _, i := range p.keys {
		switch {
		case unknown[i]:
			a.refuse(i, kcmigrate.CodeKeyUnknown.Reason(),
				fmt.Sprintf("The %s translator does not know %s; an option of a newer Kafka Connect release may change what the predicate matches.", p.pred.Base, a.ds[i].KV.Key),
				"Check the key for a typo, or keep this connector on Kafka Connect until migrate's translator covers it.")
		case i == p.refusedAt:
			a.refuse(i, p.refusal.Code, p.refusal.Reason, p.refusal.Fix)
		default:
			a.refuse(i, p.refusal.Code, "Part of the predicate "+p.alias+", which is not translated. "+p.refusal.Reason, p.refusal.Fix)
		}
	}
}

// index fills the report's transform index: one element per alias of
// transforms=, in chain order, with the worst status of its keys.
func (a *applier) index() {
	a.out.Transforms = []kcmigrate.Transform{}
	for _, l := range a.links {
		t := kcmigrate.Transform{Alias: l.alias, Status: kcmigrate.StatusTranslated}
		if l.smt != nil {
			t.Class = l.smt.Class
		}
		if len(l.keys) == 0 { // listed with no key: the transforms entry reports it
			t.Status = a.ds[a.transforms].Entry.Status
		}
		for _, i := range l.keys {
			t.Status = worse(t.Status, a.ds[i].Entry.Status)
		}
		if t.Status == kcmigrate.StatusTranslated && len(l.emitted) > 0 {
			t.Processor = a.out.Processors[l.emitted[0]].Plugin
		}
		a.out.Transforms = append(a.out.Transforms, t)
	}
}

func worse(x, y kcmigrate.Status) kcmigrate.Status {
	rank := map[kcmigrate.Status]int{kcmigrate.StatusTranslated: 0, kcmigrate.StatusApproximated: 1, kcmigrate.StatusUnsupported: 2}
	if rank[y] > rank[x] {
		return y
	}
	return x
}

func (a *applier) translated(i int, reason, path string) {
	d := &a.ds[i]
	d.Entry = kcmigrate.Entry{Key: d.Entry.Key, Status: kcmigrate.StatusTranslated, Origin: d.Entry.Origin, Reason: reason}
	if path != "" {
		d.Entry.Target = &kcmigrate.EntryTarget{ConfigPath: path}
	}
	a.decided[i] = true
}

// refuse makes the entry unsupported and blocking: transforms change data,
// so a refused transform key always blocks (invariant 6).
func (a *applier) refuse(i int, code, reason, fix string) {
	d := &a.ds[i]
	d.Entry = kcmigrate.Entry{
		Key: d.Entry.Key, Status: kcmigrate.StatusUnsupported, Origin: d.Entry.Origin,
		Code: code, Reason: reason, Fix: fix, Blocking: true,
	}
	a.decided[i] = true
}
