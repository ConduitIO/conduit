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
	"strconv"
	"strings"

	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
)

// Classes with a translator in this version, without their $Key/$Value
// suffix. Each one ships with the Kafka Connect oracle cases that prove it
// exact (tests/migrate/smt-oracle, design decision 21).
const (
	kcTransforms = "org.apache.kafka.connect.transforms."
	kcPredicates = kcTransforms + "predicates."

	classFilter                = kcTransforms + "Filter"
	classRegexRouter           = kcTransforms + "RegexRouter"
	classReplaceField          = kcTransforms + "ReplaceField"
	classExtractField          = kcTransforms + "ExtractField"
	classExtractNewRecordState = "io.debezium.transforms.ExtractNewRecordState"

	classTopicNameMatches  = kcPredicates + "TopicNameMatches"
	classHasHeaderKey      = kcPredicates + "HasHeaderKey"
	classRecordIsTombstone = kcPredicates + "RecordIsTombstone"
)

func init() {
	register(classFilter, translateFilter)
	register(classRegexRouter, translateRegexRouter)
	register(classReplaceField, translateReplaceField)
	register(classExtractField, translateExtractField)
	register(classExtractNewRecordState, translateExtractNewRecordState)
	registerPredicate(classTopicNameMatches, translateTopicNameMatches)
	registerPredicate(classHasHeaderKey, translateHasHeaderKey)
	registerPredicate(classRecordIsTombstone, translateRecordIsTombstone)
}

// Fixes many refusals share.
const (
	fixKeep      = "Keep this connector on Kafka Connect."
	fixUntilV023 = "Keep this connector on Kafka Connect until v0.23."
)

// Option names the translators read.
const (
	optReplaceNull  = "replace.null.with.default"
	optRegex        = "regex"
	optReplacement  = "replacement"
	optExclude      = "exclude"
	optInclude      = "include"
	optRenames      = "renames"
	optField        = "field"
	optSyntax       = "field.syntax.version"
	optDeleteMode   = "delete.tombstone.handling.mode"
	optPattern      = "pattern"
	optHeaderName   = "name"
	fieldAfter      = "after"
	deleteTombstone = "tombstone"
	deleteDrop      = "drop"
)

// collectionPath is what every topic expression of this version reads.
const collectionPath = Path(`.Metadata["opencdc.collection"]`)

// isDelete is true for a delete. After an unwrap point it is exactly Kafka
// Connect's tombstone: a delete's value is null there, and a native CDC
// source gives no other record a nil after-image.
const isDelete = `eq (printf "%v" .Operation) "delete"`

func refuse(option, reason, fix string) (Result, error) {
	return Result{Refusal: &Refusal{Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: reason, Fix: fix, Option: option}}, nil
}

func refuseWith(r *Refusal) (Result, error) { return Result{Refusal: r}, nil }

// variantRefusal refuses a class written with a $Key/$Value suffix it does
// not have (want false), or without the suffix it needs (want true): Kafka
// Connect cannot load either class and fails the connector.
func variantRefusal(t *SMT, want bool) *Refusal {
	switch {
	case want && t.Side == SideNone:
		return &Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: t.Class + " is abstract: Kafka Connect needs " + t.Class + "$Key or $Value and fails the connector.",
			Fix:    "Set transforms." + t.Alias + ".type to " + t.Class + "$Value (or $Key), then re-run.",
		}
	case !want && t.Side != SideNone:
		return &Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "Kafka Connect has no class " + t.Class + " and fails the connector.",
			Fix:    "Set transforms." + t.Alias + ".type to " + t.Base + ", then re-run.",
		}
	}
	return nil
}

// nullDefaultRefusal enforces decisions 13 and 19 on a Debezium source
// chain: Kafka Connect writes a column's schema default in place of a NULL
// field unless the SMT's replace.null.with.default is false, and the
// Conduit processors keep the NULL. The oracle shows the default for
// ReplaceField, InsertField, ExtractField and ValueToKey with the option
// left at true (tests/migrate/smt-oracle, README "Findings").
func nullDefaultRefusal(c *Chain, t *SMT) *Refusal {
	if !c.SchemaMode() {
		return nil
	}
	v, ok := t.Option(optReplaceNull)
	switch {
	case ok && strings.EqualFold(v, "false"):
		return nil
	case ok && !strings.EqualFold(v, "true"):
		return &Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Option: optReplaceNull,
			Reason: optReplaceNull + " is not true or false, and Kafka Connect rejects the config.",
			Fix:    "Set transforms." + t.Alias + "." + optReplaceNull + "=false, then re-run.",
		}
	}
	r := &Refusal{
		Code: kcmigrate.CodeSettingRequired.Reason(),
		Reason: optReplaceNull + " is true (its default when unset), so on a Debezium source Kafka Connect writes a column's schema default " +
			"in place of a NULL field, and the Conduit processors keep the NULL.",
		Fix: "Set transforms." + t.Alias + "." + optReplaceNull + "=false if consumers should see the NULL (it changes what Kafka Connect writes too), then re-run; or keep this connector on Kafka Connect.",
	}
	if ok {
		r.Option = optReplaceNull
	}
	return r
}

// translateFilter: Kafka Connect's Filter drops every record its predicate
// matches, and every record when it has none. A filter processor does the
// same with the predicate as its condition (the chain ANDs it in).
func translateFilter(_ *Chain, t *SMT) (Result, error) {
	if r := variantRefusal(t, false); r != nil {
		return refuseWith(r)
	}
	return Result{Processors: []Processor{{Plugin: "filter"}}}, nil
}

// translateRegexRouter rewrites the topic expression (design, "Topic
// expression"): matches() then replaceFirst() on the topic, with the
// pattern and replacement translated (regex.go). With a predicate the
// topic is rewritten only for the records it matches. The expression is
// evaluated against the final record, so the chain refuses the router when
// a later transform writes what it reads.
func translateRegexRouter(c *Chain, t *SMT) (Result, error) {
	if r := variantRefusal(t, false); r != nil {
		return refuseWith(r)
	}
	pattern, ok := t.Option(optRegex)
	if !ok || pattern == "" {
		at := ""
		if ok {
			at = optRegex
		}
		return refuse(at, "regex is not set, and Kafka Connect rejects the config.", "Set transforms."+t.Alias+".regex, then re-run.")
	}
	replacement, ok := t.Option(optReplacement)
	if !ok {
		return refuse("", "replacement is not set, and Kafka Connect rejects the config.", "Set transforms."+t.Alias+".replacement, then re-run.")
	}
	topic, known := c.Topic()
	if !known {
		return refuseWith(c.TopicRefusal())
	}
	if c.Capability(roleDestination).TopicSetting == "" {
		return Result{Refusal: &Refusal{
			Code:   kcmigrate.CodeTargetCapabilityMissing.Reason(),
			Reason: "The destination " + c.env.Destination + " has no setting that names a Kafka topic, so a rewritten topic has nowhere to go.",
			Fix:    "Keep this connector on Kafka Connect, or remove the transform if the destination's own naming is acceptable.",
		}}, nil
	}
	re, why := translatePattern(pattern)
	if why != "" {
		return refuse(optRegex, "The regex is not translated: "+why+".", regexFix(t.Alias, optRegex))
	}
	rep, why := translateReplacement(replacement, re)
	if why != "" {
		return refuse(optReplacement, "The replacement is not translated: "+why+".", regexFix(t.Alias, optReplacement))
	}
	route := RegexRoute(re.pattern, rep, topic)
	if t.Predicate != "" {
		route = "ternary (" + route + ") (" + topic + ") (" + t.PredicateCondition + ")"
	}
	c.SetTopic(route)
	return Result{
		Claimed:       []string{optRegex, optReplacement},
		Reason:        "Rewrites the topic the destination writes to, as Kafka Connect's matches() then replaceFirst() does.",
		Reads:         []Path{collectionPath},
		UsesPredicate: t.Predicate != "",
	}, nil
}

func regexFix(alias, opt string) string {
	return "Rewrite transforms." + alias + "." + opt + " with constructs that mean the same in Java and Go (literals, classes, \\d \\w \\s, ^ $ \\b, quantifiers, groups and (?i)), or keep this connector on Kafka Connect."
}

// fieldName is a field name the processors can reference as
// .Payload.After.<name> and field.rename can take as a new name.
var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// javaList splits a Kafka Connect LIST value (already trimmed): an empty
// value is an empty list.
func javaList(v string) []string {
	if v == "" {
		return nil
	}
	return listSplit.Split(v, -1)
}

// translateReplaceField translates ReplaceField$Value's exclude and renames
// after an unwrap point on a Debezium source (design, "Mapping and
// exactness"): field.exclude, then one field.rename per mapping, on
// .Payload.After and .Payload.Before.
//
// Kafka Connect's schema mode builds the new value from the original
// field names: a field is dropped when excluded, renamed when mapped, and
// each output field is read back through the reverse mapping. So a record
// fails when a rename target is already a field (it is a duplicate if the
// source is there too, and the reverse lookup of the target names a source
// the record lacks otherwise); the After copy fails such a record with an
// error processor, as Kafka Connect does. Renames run in sequence in
// Conduit and at once in Kafka Connect, so a target that is also a source,
// two mappings of one source or onto one target, and a renamed source that
// is also excluded are refused.
func translateReplaceField(c *Chain, t *SMT) (Result, error) {
	if r := variantRefusal(t, true); r != nil {
		return refuseWith(r)
	}
	if t.Side == SideKey {
		return Result{Refusal: &Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "ReplaceField$Key is mapped in v0.23; no oracle case proves the key translation yet.",
			Fix:    fixUntilV023,
		}}, nil
	}
	if c.SchemaCarrying(SideValue) {
		return refuseWith(SchemaRefusal())
	}
	if r := structuredRowRefusal(c, t); r != nil {
		return refuseWith(r)
	}
	if r := nullDefaultRefusal(c, t); r != nil {
		return refuseWith(r)
	}
	claimed := []string{}
	if _, ok := t.Option(optReplaceNull); ok {
		claimed = append(claimed, optReplaceNull)
	}
	if v, ok := t.Option(optInclude); ok {
		if len(javaList(v)) > 0 {
			return Result{Refusal: &Refusal{
				Code: kcmigrate.CodeSMTUnsupported.Reason(), Option: optInclude,
				Reason: "ReplaceField include is mapped in v0.23 (design decision 7).",
				Fix:    "Keep this connector on Kafka Connect until v0.23, or express the same with exclude and re-run.",
			}}, nil
		}
		claimed = append(claimed, optInclude) // an empty include keeps every field
	}
	var exclude []string
	if v, ok := t.Option(optExclude); ok {
		claimed = append(claimed, optExclude)
		for _, f := range javaList(v) {
			if !fieldName.MatchString(f) {
				return refuse(optExclude, fmt.Sprintf("The field name %q is not translated: only names of letters, digits and underscores are.", f), "Keep this connector on Kafka Connect until a migrate release translates such names.")
			}
			exclude = append(exclude, f)
		}
	}
	renames, r := parseRenames(t, exclude)
	if r != nil {
		return refuseWith(r)
	}
	if _, ok := t.Option(optRenames); ok {
		claimed = append(claimed, optRenames)
	}
	var res Result
	res.Claimed = claimed
	if len(exclude) > 0 {
		res.Processors = append(res.Processors, BothImages(func(img string) Processor {
			fields := make([]string, len(exclude))
			for i, f := range exclude {
				fields[i] = img + "." + f
				res.Writes = append(res.Writes, Path(fields[i]))
			}
			p := Processor{Plugin: "field.exclude", Settings: map[string]string{"fields": strings.Join(fields, ",")}, From: []string{optExclude}, Reshapes: SideValue}
			if img == AfterImage {
				p.Condition = NilGuard(SideValue) // Kafka Connect passes a null value through
			}
			return p
		})...)
	}
	for _, m := range renames {
		from, to := AfterImage+"."+m[0], AfterImage+"."+m[1]
		bfrom, bto := BeforeImage+"."+m[0], BeforeImage+"."+m[1]
		res.Writes = append(res.Writes, Path(from), Path(to), Path(bfrom), Path(bto))
		res.Processors = append(res.Processors,
			failIfTarget(t.Alias, m[1]),
			Processor{
				Plugin: "field.rename", Settings: map[string]string{"mapping": from + ":" + m[1]},
				Condition: FieldPresentAt(AfterImage, m[0]), From: []string{optRenames}, Reshapes: SideValue, NeedsDecoded: SideValue,
			},
			// The before-image has no Kafka Connect counterpart (decision
			// 12): it is renamed only where the rename succeeds, so a
			// delete, whose value Kafka Connect passes as null, never fails.
			Processor{
				Plugin: "field.rename", Settings: map[string]string{"mapping": bfrom + ":" + m[1]},
				Condition: And(notNil(BeforeImage), FieldPresentAt(BeforeImage, m[0]), Not(FieldPresentAt(BeforeImage, m[1]))),
				From:      []string{optRenames}, Reshapes: SideValue, NeedsDecoded: SideValue,
			},
		)
	}
	if len(res.Processors) == 0 {
		res.Reason = "No field is excluded or renamed, and Kafka Connect passes the record through unchanged."
	}
	return res, nil
}

// failIfTarget fails a record whose after-image already has the rename
// target, as Kafka Connect does. Its condition reads the after-image's
// fields, so the side is decoded first (Processor.NeedsDecoded).
func failIfTarget(alias, target string) Processor {
	p := FailRecord(FieldPresentAt(AfterImage, target), fmt.Sprintf(
		"transform %s (ReplaceField): the rename target %s is already a field of the record, which Kafka Connect fails", alias, target))
	p.From, p.NeedsDecoded = []string{optRenames}, SideValue
	return p
}

// parseRenames reads ReplaceField's renames (old:new pairs) and refuses
// what does not translate exactly.
func parseRenames(t *SMT, exclude []string) ([][2]string, *Refusal) {
	v, ok := t.Option(optRenames)
	if !ok {
		return nil, nil
	}
	bad := func(reason string) *Refusal {
		return &Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Option: optRenames, Reason: reason,
			Fix: "Rename in two steps (two transforms) or keep this connector on Kafka Connect.",
		}
	}
	var out [][2]string
	sources, targets := map[string]bool{}, map[string]bool{}
	for _, m := range javaList(v) {
		parts := strings.Split(m, ":")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, &Refusal{
				Code: kcmigrate.CodeSMTUnsupported.Reason(), Option: optRenames,
				Reason: fmt.Sprintf("The mapping %q is not old:new, and Kafka Connect rejects it.", m),
				Fix:    "Write each mapping as old:new, then re-run.",
			}
		}
		from, to := parts[0], parts[1]
		switch {
		case !fieldName.MatchString(from) || !fieldName.MatchString(to):
			return nil, bad(fmt.Sprintf("The mapping %q is not translated: only names of letters, digits and underscores are.", m))
		case from == to:
			return nil, bad(fmt.Sprintf("The mapping %q renames a field to itself.", m))
		case sources[from]:
			return nil, bad(fmt.Sprintf("%s is renamed twice.", from))
		case targets[to]:
			return nil, bad(fmt.Sprintf("Two fields are renamed to %s.", to))
		case contains(exclude, from):
			return nil, bad(fmt.Sprintf("%s is both excluded and renamed; Kafka Connect then reads the excluded field's value into the rename target.", from))
		}
		sources[from], targets[to] = true, true
		out = append(out, [2]string{from, to})
	}
	for _, m := range out {
		if sources[m[1]] {
			return nil, bad(fmt.Sprintf("%s is both a rename target and a rename source; Kafka Connect renames all fields at once from their original names, Conduit in sequence.", m[1]))
		}
	}
	return out, nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// structuredRowRefusal refuses a value-side reshaping SMT anywhere but on
// the row of a Debezium source after an unwrap point: before it the value
// is the Debezium envelope, which native CDC records do not have, and on
// the other chains of this version the value is raw bytes, on which Kafka
// Connect fails the SMT.
func structuredRowRefusal(c *Chain, t *SMT) *Refusal {
	switch {
	case c.DebeziumOutput():
		return &Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "The pipeline writes Debezium-compatible output, where SMTs that reshape the value are mapped in v0.23 (design decision 5).",
			Fix:    fixUntilV023,
		}
	case !c.SchemaMode() || c.Shape(SideValue) != ShapeStructured:
		return &Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: t.Base + " needs a structured value, and on this connector the value is raw bytes (or its shape is unknown), on which Kafka Connect fails it.",
			Fix:    fixKeep,
		}
	case !c.Unwrapped():
		return &Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "The transform runs on the Debezium envelope (before, after, source, op), which the Conduit Postgres source does not emit: its records are the row. " +
				"It is translated only after ExtractNewRecordState or ExtractField$Value with field=after.",
			Fix: "Move the transform after the unwrap, or keep this connector on Kafka Connect.",
		}
	}
	return nil
}

// unwrapRefusal refuses an unwrap point (ExtractNewRecordState, ExtractField
// of after) anywhere but on a Debezium source chain in OpenCDC output.
func unwrapRefusal(c *Chain, t *SMT) *Refusal {
	switch {
	case c.DebeziumOutput():
		return &Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "The pipeline writes Debezium-compatible output, where the unwrap is mapped in v0.23 (design decision 5).",
			Fix:    fixUntilV023,
		}
	case !c.SchemaMode():
		return &Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "The transform unwraps a Debezium envelope, and on this connector the value is not one in Conduit: it is raw bytes " +
				"(on a sink, unwrap.debezium would need the payload wrapper of schemas.enable=true, which this version refuses).",
			Fix: fixKeep,
		}
	case t.Predicate != "":
		return &Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "The unwrap has a predicate, so Kafka Connect leaves the envelope on the records it does not match, which native CDC records cannot reproduce.",
			Fix:    "Remove the predicate from the unwrap, or keep this connector on Kafka Connect.",
		}
	}
	return nil
}

// translateExtractNewRecordState: on a Debezium source the Conduit
// Postgres source already emits the row, so the default mode (tombstone: a
// delete becomes a null value, which a native delete's nil after-image
// already is) needs no processor, and drop mode drops deletes with a filter.
// Debezium's own tombstones have no native counterpart; the
// kc.output_shape_differs note covers them.
func translateExtractNewRecordState(c *Chain, t *SMT) (Result, error) {
	if r := variantRefusal(t, false); r != nil {
		return refuseWith(r)
	}
	if r := unwrapRefusal(c, t); r != nil {
		return refuseWith(r)
	}
	if r := nullDefaultRefusal(c, t); r != nil {
		return refuseWith(r)
	}
	for _, o := range t.Options() {
		switch o.Name {
		case optReplaceNull, optDeleteMode:
		case "add.fields", "add.fields.prefix", "add.headers", "add.headers.prefix",
			"drop.fields.header.name", "drop.fields.from.key", "drop.fields.keep.schema.compatible", "route.by.field":
			return refuse(o.Name, "ExtractNewRecordState's "+o.Name+" is mapped in v0.23.", "Keep this connector on Kafka Connect until v0.23, or remove "+o.Key+" and re-run.")
		case "delete.handling.mode", "drop.tombstones":
			return refuse(o.Name, o.Name+" is not an option of ExtractNewRecordState in Debezium 3.7, which ignores it, so the behavior it asks for does not happen in Kafka Connect either.",
				"Remove "+o.Key+" (use "+optDeleteMode+"), then re-run.")
		}
	}
	res := Result{Unwraps: true, Claimed: []string{}}
	if _, ok := t.Option(optReplaceNull); ok {
		res.Claimed = append(res.Claimed, optReplaceNull)
	}
	mode := deleteTombstone
	if v, ok := t.Option(optDeleteMode); ok {
		res.Claimed = append(res.Claimed, optDeleteMode)
		mode = v
	}
	switch mode {
	case deleteTombstone:
		res.Reason = "No processor: the Conduit Postgres source already emits the row, and a delete's after-image is nil, as Kafka Connect's value is null in tombstone mode."
	case deleteDrop:
		res.Processors = []Processor{{Plugin: "filter", Condition: isDelete, From: []string{optDeleteMode}}}
		res.DropsDeletes = true
	default:
		return refuse(optDeleteMode, fmt.Sprintf("%s=%s is mapped in v0.23 (only tombstone and drop translate).", optDeleteMode, mode),
			"Keep this connector on Kafka Connect until v0.23, or use tombstone or drop.")
	}
	return res, nil
}

// translateExtractField translates ExtractField$Value with field=after on a
// Debezium source before any unwrap point: Kafka Connect's value becomes the
// row (null for a delete), which the native CDC record already is. Every
// other field is refused: on debezium-postgres every column is a scalar,
// which cannot become an OpenCDC side without changing its bytes.
func translateExtractField(c *Chain, t *SMT) (Result, error) {
	if r := variantRefusal(t, true); r != nil {
		return refuseWith(r)
	}
	if t.Side == SideKey {
		return refuse("", "ExtractField$Key is mapped in v0.23.", fixUntilV023)
	}
	if r := unwrapRefusal(c, t); r != nil {
		return refuseWith(r)
	}
	field, ok := t.Option(optField)
	switch {
	case !ok || field == "":
		return refuse("", "field is not set, and Kafka Connect rejects the config.", "Set transforms."+t.Alias+".field, then re-run.")
	case field != fieldAfter || c.Unwrapped():
		return refuse(optField, "Only field=after on the Debezium envelope translates: a column is a scalar, which cannot become a record value in Conduit without changing its bytes (v0.23).",
			"Keep this connector on Kafka Connect, or use ExtractNewRecordState.")
	}
	res := Result{Unwraps: true, Claimed: []string{optField}}
	if v, ok := t.Option(optSyntax); ok {
		if v != "V1" && v != "V2" {
			return refuse(optSyntax, optSyntax+" is not V1 or V2.", "Set "+optSyntax+" to V1 or V2, then re-run.")
		}
		res.Claimed = append(res.Claimed, optSyntax) // after has no dot: both versions read it alike
	}
	if r := nullDefaultRefusal(c, t); r != nil {
		return refuseWith(r)
	}
	if _, ok := t.Option(optReplaceNull); ok {
		res.Claimed = append(res.Claimed, optReplaceNull)
	}
	res.Reason = "No processor: the Conduit Postgres source already emits the row as the record's after-image, and a delete's is nil, as Kafka Connect's value is null."
	return res, nil
}

// translateTopicNameMatches: Kafka Connect matches the whole topic against
// the pattern. The condition does the same on the topic expression at the
// transform's position.
func translateTopicNameMatches(c *Chain, p *Predicate) (PredicateResult, error) {
	pattern, ok := p.Option(optPattern)
	if !ok || pattern == "" {
		return PredicateResult{Refusal: &Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: "pattern is not set, and Kafka Connect rejects the config.",
			Fix: "Set predicates." + p.Alias + ".pattern, then re-run.",
		}}, nil
	}
	topic, known := c.Topic()
	if !known {
		return PredicateResult{Refusal: c.TopicRefusal()}, nil
	}
	re, why := translatePattern(pattern)
	if why != "" {
		return PredicateResult{Refusal: &Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Option: optPattern,
			Reason: "The pattern is not translated: " + why + ".", Fix: regexFix(p.Alias, optPattern),
		}}, nil
	}
	return PredicateResult{
		Condition: "regexMatch `^(?:" + re.pattern + ")$` (" + topic + ")",
		Claimed:   []string{optPattern},
		Reads:     []Path{collectionPath},
	}, nil
}

// translateHasHeaderKey: Kafka Connect matches a record with at least one
// header of that name, whatever its value (null included). Outside mirror
// mode the Kafka source writes every header as metadata
// kafka.header.<name> (a null value as ""), so the predicate is "that
// metadata key is present". A template cannot test a key of
// opencdc.Metadata for presence directly (index returns "" for a missing
// key and for an empty value alike), so the condition looks for the
// JSON-encoded key followed by a colon in the JSON-encoded metadata: JSON
// escapes every quote inside a string, so that text occurs only as an
// object key.
func translateHasHeaderKey(c *Chain, p *Predicate) (PredicateResult, error) {
	name, ok := p.Option(optHeaderName)
	if !ok || name == "" {
		return PredicateResult{Refusal: &Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: "name is not set, and Kafka Connect rejects the config.",
			Fix: "Set predicates." + p.Alias + ".name, then re-run.",
		}}, nil
	}
	prefix := c.Capability(roleSource).HeaderPrefix
	switch {
	case c.Scope() != rules.ScopeSink:
		return PredicateResult{Refusal: &Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "On a source connector the headers are the ones its records are produced with (Debezium headers, MirrorMaker's replicated headers), which the Conduit source does not carry alike; this is mapped in v0.23.",
			Fix:    fixUntilV023,
		}}, nil
	case prefix == "":
		return PredicateResult{Refusal: &Refusal{
			Code:   kcmigrate.CodeTargetCapabilityMissing.Reason(),
			Reason: "The source " + c.env.Source + " does not carry Kafka headers into the record metadata.",
			Fix:    fixKeep,
		}}, nil
	case c.MirrorMetadata():
		return PredicateResult{Refusal: &Refusal{
			Code:   kcmigrate.CodeTargetCapabilityMissing.Reason(),
			Reason: "The Kafka source runs in mirror mode, which leaves out the metadata entry of a header that is not UTF-8.",
			Fix:    fixKeep,
		}}, nil
	case strings.ContainsRune(name, '�'):
		return PredicateResult{Refusal: &Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Option: optHeaderName,
			Reason: "The header name contains U+FFFD, which the condition cannot tell apart from a header name that is not UTF-8.",
			Fix:    fixKeep,
		}}, nil
	}
	return PredicateResult{
		Condition: `contains (print (toJson ` + strconv.Quote(prefix+name) + `) ":") (toJson .Metadata)`,
		Claimed:   []string{optHeaderName},
		Reads:     []Path{".Metadata"},
	}, nil
}

// translateRecordIsTombstone: Kafka Connect matches a record whose value is
// null. On a Debezium source before an unwrap point that is only Debezium's
// own tombstone after a delete, which native CDC does not emit (the
// output-shape note covers it), so nothing matches. After an unwrap point
// in tombstone mode, or after ExtractField of after, it is exactly a delete;
// after drop mode there is none. A sink or MirrorMaker chain reads records
// through the Kafka source, which outside mirror mode writes a null Kafka
// value and an empty one alike, so it is refused there.
func translateRecordIsTombstone(c *Chain, p *Predicate) (PredicateResult, error) {
	if !c.SchemaMode() {
		return PredicateResult{Refusal: &Refusal{
			Code:   kcmigrate.CodeTargetCapabilityMissing.Reason(),
			Reason: "The Kafka source writes a null Kafka value and an empty one alike, so a tombstone cannot be told from an empty record.",
			Fix:    fixKeep,
		}}, nil
	}
	res := PredicateResult{Claimed: []string{}, Reads: []Path{".Operation"}}
	switch {
	case !c.Unwrapped(), c.DeletesDropped():
		res.Condition = "false"
	default:
		res.Condition = isDelete
	}
	return res, nil
}
