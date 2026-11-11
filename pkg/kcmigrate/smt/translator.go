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
	"strconv"
	"strings"

	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
)

// Translator returns the exact processors for one SMT, or a refusal. The
// error is for bugs only (bad rule data, an impossible state); it fails the
// run as an internal error. A translator must refuse every configuration
// outside the subset where its processors behave exactly as the SMT does
// (emit-if-exact): it never approximates.
type Translator func(c *Chain, t *SMT) (Result, error)

// PredicateTranslator returns the condition expression that is true exactly
// for the records the predicate matches, or a refusal. The error is for bugs
// only.
type PredicateTranslator func(c *Chain, p *Predicate) (PredicateResult, error)

// Result is a translator's verdict on one SMT.
type Result struct {
	// Processors are the SMT's processors, in order. Empty with Refusal
	// set, and empty for an SMT that needs no processor (Reason says why).
	Processors []Processor
	// Refusal is set when the configuration is outside the exact subset.
	Refusal *Refusal
	// Claimed are the option names (the part of the key after
	// transforms.<alias>.) the translator consumed. An option it does not
	// claim is kc.key_unknown and blocking, so an option added by a later
	// Kafka Connect release is never ignored.
	Claimed []string
	// Reason explains a translation that writes no processor. Required
	// when Processors and Refusal are both empty.
	Reason string
	// Reads are the record paths the SMT's emitted logic reads where it is
	// evaluated against the final record rather than at its position: the
	// topic expression (Chain.SetTopic). The chain refuses the SMT when a
	// later processor writes one of them (design, "Topic expression").
	Reads []Path
	// Writes are the record paths the SMT's processors write; a whole side
	// (.Payload.After) when the translator cannot name the fields.
	Writes []Path
	// UsesPredicate means the translator folded SMT.PredicateCondition into
	// what it emits (a predicated RegexRouter's topic expression). Without
	// it, a predicated SMT that writes no processor is refused: its
	// predicate has no processor condition to become.
	UsesPredicate bool
	// Unwraps marks an unwrap point: ExtractNewRecordState, or
	// ExtractField$Value with field=after (design decision 18). After it, Kafka
	// Connect's value is the row, not the Debezium envelope, and translators
	// read Chain.Unwrapped to apply the after-unwrap rules. A second unwrap
	// point in one chain is refused.
	Unwraps bool
}

// Path is a record reference, such as .Payload.After.id, .Key or
// .Metadata["opencdc.collection"].
type Path string

// overlaps reports whether one path is the other or contains it.
func (p Path) overlaps(q Path) bool {
	a, b := string(p), string(q)
	if len(a) > len(b) {
		a, b = b, a
	}
	return a == b || (strings.HasPrefix(b, a) && (b[len(a)] == '.' || b[len(a)] == '['))
}

// PredicateResult is a predicate translator's verdict.
type PredicateResult struct {
	// Condition is a Go template pipeline (without the braces) that
	// evaluates to true for the records the predicate matches. Empty with
	// Refusal set.
	Condition string
	Refusal   *Refusal
	// Claimed are the option names the translator consumed (the part of
	// the key after predicates.<alias>.).
	Claimed []string
	// Reads are the record paths the condition reads, checked against later
	// writes when a translator folds the predicate into the topic.
	Reads []Path
}

// Processor is one processor a translator emits.
type Processor struct {
	// Plugin is the processor plugin, for example filter or field.set.
	Plugin   string
	Settings map[string]string
	// Condition is an optional Go template pipeline (without the braces),
	// such as a nil guard (NilGuard). The chain ANDs it with the SMT's
	// predicate.
	Condition string
	// From names the options this processor implements, so their report
	// entries point at it. An option no processor names points at the
	// SMT's first processor.
	From []string
	// Reshapes is the side whose shape the processor changes, SideNone if
	// it changes no shape. Before such a processor the chain removes the
	// side's schema subject and version from the record metadata, under the
	// same condition, so the processor's encode middleware never registers
	// a schema extracted from the reshaped data as a new version of the
	// source's subject (design, "Schema metadata after reshaping").
	Reshapes Side
}

// Refusal says why an SMT, or one of its keys, is not translated. Every
// refusal is blocking.
type Refusal struct {
	Code   string
	Reason string
	Fix    string
	// Option is the option the reason is about; empty means the SMT as a
	// whole, reported on its type key.
	Option string
}

// Registry maps an SMT or predicate class, without its $Key/$Value
// suffix, to its translator. A class with no translator is refused with the
// smt data's refusal for it.
type Registry struct {
	Transforms map[string]Translator
	Predicates map[string]PredicateTranslator
}

// Builtin returns the translators this version ships. It has none yet: a
// translator is added together with the Kafka Connect oracle output that
// proves it exact (design, "Tests"), so until then every SMT class is
// refused with its reason from the smt data.
func Builtin() *Registry {
	return &Registry{Transforms: map[string]Translator{}, Predicates: map[string]PredicateTranslator{}}
}

// Side is the part of a record an SMT operates on, from its class suffix.
type Side string

const (
	// SideNone is a class without a $Key or $Value variant.
	SideNone Side = ""
	// SideKey is a $Key variant: the record key, .Key.
	SideKey Side = "key"
	// SideValue is a $Value variant: the record value, .Payload.After.
	SideValue Side = "value"
)

// Field returns the record reference of the side: .Key or .Payload.After.
// Empty for SideNone.
func (s Side) Field() string {
	switch s {
	case SideKey:
		return ".Key"
	case SideValue:
		return AfterImage
	case SideNone:
	}
	return ""
}

// splitClass strips a $Key or $Value suffix.
func splitClass(class string) (string, Side) {
	switch {
	case strings.HasSuffix(class, "$Key"):
		return strings.TrimSuffix(class, "$Key"), SideKey
	case strings.HasSuffix(class, "$Value"):
		return strings.TrimSuffix(class, "$Value"), SideValue
	default:
		return class, SideNone
	}
}

// Option is one option key of an SMT or predicate.
type Option struct {
	// Name is the part of the key after transforms.<alias>. (or
	// predicates.<alias>.).
	Name string
	// Key is the full input key.
	Key   string
	value kcmigrate.Value
}

// Value returns the option's value, trimmed as Kafka Connect's
// ConfigDef.parseType trims string values. Translators compare it or copy
// it into a processor setting; it never reaches the report.
func (o Option) Value() string { return strings.TrimSpace(o.value.Reveal()) }

// SMT is one transform of a chain, as a translator sees it.
type SMT struct {
	Alias string
	// Class is the class as written (trimmed), with its suffix.
	Class string
	// Base is Class without its $Key/$Value suffix.
	Base string
	Side Side
	// Predicate is the alias of the transform's predicate, empty if none.
	// The chain turns it, with Negate, into the processors' condition.
	Predicate string
	Negate    bool
	// PredicateCondition is the condition pipeline of the predicate, with
	// Negate applied, for a translator that writes no processor and folds
	// it in itself (Result.UsesPredicate). Empty without a predicate.
	PredicateCondition string
	options            []Option
}

// Options returns the SMT's option keys in input order. The type,
// predicate and negate keys are the chain's and are not among them.
func (t *SMT) Options() []Option { return t.options }

// Option returns the trimmed value of the named option.
func (t *SMT) Option(name string) (string, bool) { return findOption(t.options, name) }

// Predicate is one predicate a transform names.
type Predicate struct {
	Alias   string
	Class   string
	Base    string
	options []Option
}

// Options returns the predicate's option keys in input order, without
// type.
func (p *Predicate) Options() []Option { return p.options }

// Option returns the trimmed value of the named option.
func (p *Predicate) Option(name string) (string, bool) { return findOption(p.options, name) }

func findOption(opts []Option, name string) (string, bool) {
	val, ok := "", false
	for _, o := range opts {
		if o.Name == name {
			val, ok = o.Value(), true // the last occurrence wins; earlier ones are duplicates
		}
	}
	return val, ok
}

// Chain is the state a translator reads: the connector the chain runs on
// and the topic expression at the translator's position.
type Chain struct {
	env       Env
	data      *Data
	topic     string
	known     bool
	unwrapped bool
}

// Scope is rules.ScopeSource or rules.ScopeSink: whether the chain runs in
// a source connector (before the producer) or a sink (after the consumer).
func (c *Chain) Scope() string { return c.env.Scope }

// SchemaMode reports whether Kafka Connect runs the chain in schema mode
// (Env.SchemaMode).
func (c *Chain) SchemaMode() bool { return c.env.SchemaMode }

// DebeziumOutput reports whether the pipeline writes Debezium-compatible
// output.
func (c *Chain) DebeziumOutput() bool { return c.env.DebeziumOutput }

// SchemaCarrying reports whether the side's Kafka Connect converter
// carries a schema, or is unknown (Env.KeySchema). A translator whose
// processors reshape the side refuses with SchemaRefusal when it does.
func (c *Chain) SchemaCarrying(s Side) bool {
	switch s {
	case SideKey:
		return c.env.KeySchema
	case SideValue:
		return c.env.ValueSchema
	case SideNone:
	}
	return c.env.KeySchema || c.env.ValueSchema
}

// SchemaRefusal is decision 8's refusal of an SMT that reshapes a side
// whose converter carries a schema.
func SchemaRefusal() *Refusal {
	return &Refusal{
		Code:   kcmigrate.CodeSMTUnsupported.Reason(),
		Reason: "A schema-carrying converter needs a type-preserving schema for the reshaped data; Conduit would register one with widened types, so this is refused until v0.23.",
		Fix:    "Keep this connector on Kafka Connect until v0.23.",
	}
}

// Shape returns what the side holds where the chain starts.
func (c *Chain) Shape(s Side) Shape {
	switch s {
	case SideKey:
		return c.env.KeyShape
	case SideValue:
		return c.env.ValueShape
	case SideNone:
	}
	return ShapeUnknown
}

// Capability returns the capabilities of the pipeline's plugin in role
// ("source" or "destination").
func (c *Chain) Capability(role string) Capability {
	plugin := c.env.Destination
	if role == rules.ScopeSource {
		plugin = c.env.Source
	}
	return c.data.Capability(plugin, role)
}

// Topic returns the topic at this position of the chain as a Go template
// pipeline (without the braces), and false when the connector rules left it
// unset or it cannot be read as one pipeline. A translator that reads the
// topic must refuse when it is unknown.
func (c *Chain) Topic() (string, bool) { return c.topic, c.known }

// SetTopic replaces the topic expression for the transforms after this
// one, as RegexRouter does. It takes effect only if the translator's result
// is not a refusal.
func (c *Chain) SetTopic(expr string) { c.topic, c.known = expr, expr != "" }

// Unwrapped reports whether an earlier SMT of the chain was an unwrap point
// (Result.Unwraps): the value is the row, not the Debezium envelope.
func (c *Chain) Unwrapped() bool { return c.unwrapped }

// NilGuard returns a condition pipeline that is false only when the side
// holds a nil interface (a delete's .Payload.After, a tombstone's key or
// value), so a processor ANDed with it skips such records and never turns
// them into an empty structured value (#3147). RawData(nil) and an empty
// StructuredData are not nil: Kafka Connect hands an empty value to the
// transform too. text/template's eq would treat RawData(nil) as nil, so the
// guard compares the dynamic type instead.
func NilGuard(s Side) string { return notNil(s.Field()) }

// References of a change's images.
const (
	AfterImage  = ".Payload.After"
	BeforeImage = ".Payload.Before"
)

func notNil(field string) string {
	if field == "" {
		return ""
	}
	return `ne (printf "%T" ` + field + `) "<nil>"`
}

// BothImages returns a value-side processor for .Payload.After and a copy
// for .Payload.Before (design decision 12), from build, which writes the
// processor for one image reference. The After copy is as build wrote it,
// following Kafka Connect's null-side behaviour; the Before copy is always
// nil-guarded on top of it (decision 17), because a nil before-image means
// "no image", not a Kafka Connect null, and must stay nil.
func BothImages(build func(image string) Processor) []Processor {
	after, before := build(SideValue.Field()), build(BeforeImage)
	before.Condition = And(before.Condition, notNil(BeforeImage))
	return []Processor{after, before}
}

// FieldPresent returns a condition pipeline that is true when the side
// holds structured data with the top-level field name, present even with a
// null value (a present null field is renamed in Kafka Connect), so a
// processor
// ANDed with it leaves a record without the field alone (field.rename would
// otherwise write name: nil where Kafka Connect renames nothing). sprig's
// hasKey takes the StructuredData map; the type test before it keeps hasKey
// from failing on raw data, and and stops at the first false argument.
func FieldPresent(s Side, name string) string {
	if s.Field() == "" {
		return ""
	}
	return `and (eq (printf "%T" ` + s.Field() + `) "opencdc.StructuredData") (hasKey ` + s.Field() + ` ` + strconv.Quote(name) + `)`
}

// FailRecord returns an error processor that fails every record matching
// cond, with a fixed message (no record value), for a record Kafka Connect
// fails too: the record goes to the DLQ or halts the pipeline per
// errors.tolerance, never silently through (invariant 3).
func FailRecord(cond, message string) Processor {
	return Processor{Plugin: "error", Condition: cond, Settings: map[string]string{"message": strings.ReplaceAll(strings.ReplaceAll(message, "{{", "{ {"), "}}", "} }")}}
}

// RegexRoute returns the topic pipeline of a RegexRouter over the topic
// pipeline t: when the whole topic matches the pattern, the first match
// from the start is replaced, as Kafka Connect's matches() then
// replaceFirst() does (a|ab on ab replaces a, giving Xb); otherwise the topic
// is kept. Anchoring only at the start makes RE2's leftmost-first match the
// one Java's replaceFirst finds. pattern and replacement must already be
// translated to RE2 and Go's expansion syntax, and contain no backtick;
// they are written as raw strings, since "\d" is a template parse error.
func RegexRoute(pattern, replacement, t string) string {
	return "ternary (regexReplaceAll `^(?:" + pattern + ")` (" + t + ") `" + replacement + "`) (" + t + ") (regexMatch `^(?:" + pattern + ")$` (" + t + "))"
}

// Not negates a condition pipeline.
func Not(cond string) string { return "not (" + cond + ")" }

// And combines condition pipelines; empty ones are left out. text/template's
// and stops at the first false argument.
func And(conds ...string) string {
	var parts []string
	for _, c := range conds {
		if c != "" {
			parts = append(parts, c)
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return "and (" + strings.Join(parts, ") (") + ")"
}

// Template turns a condition pipeline into the processor condition setting.
func Template(cond string) string {
	if cond == "" {
		return ""
	}
	return "{{ " + cond + " }}"
}

// TopicPipeline reads a topic setting written by the connector rules as one
// template pipeline: a literal topic becomes a quoted string, and a setting
// that is exactly one {{ ... }} action becomes its pipeline. Anything else
// (text around an action, several actions) is not read: false.
func TopicPipeline(setting string) (string, bool) {
	s := strings.TrimSpace(setting)
	switch {
	case s == "":
		return "", false
	case !strings.Contains(s, "{{") && !strings.Contains(s, "}}"):
		return strconv.Quote(s), true // Go template string literals are Go string literals
	case strings.HasPrefix(s, "{{") && strings.HasSuffix(s, "}}") && strings.Count(s, "{{") == 1 && strings.Count(s, "}}") == 1:
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "{{"), "}}"))
		if inner == "" || strings.HasPrefix(inner, "-") || strings.HasSuffix(inner, "-") {
			return "", false
		}
		return inner, true
	}
	return "", false
}
