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

package replay

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/conduitio/conduit-commons/opencdc"
	smtoracle "github.com/conduitio/conduit/tests/migrate/smt-oracle"
)

// Result kinds of a View, the same words as smtoracle.Result.
const (
	ViewRecord  = smtoracle.ResultRecord
	ViewDropped = smtoracle.ResultDropped
	ViewError   = smtoracle.ResultError
)

// Tables of the fixture database that the options and tests name.
const (
	TableItems  = "items"
	TableStrict = "strict"
	TableToast  = "toast"
)

// ToastPlaceholder is what Debezium writes for a TOASTed column that an
// update did not change.
const ToastPlaceholder = "__debezium_unavailable_value"

// View is one record in a form both sides can be compared in: what is left
// after the chain, decoded from JSON, with numbers kept exact (json.Number).
type View struct {
	// Kind is ViewRecord, ViewDropped or ViewError.
	Kind string
	// Key and Value are decoded JSON: nil, bool, string, json.Number,
	// []any or map[string]any. A tombstone or a deleted row is nil.
	Key, Value any
	// Topic is the topic the record ends up with. On the Kafka Connect side it
	// is the output topic and InputTopic the topic the record came in with; on
	// the Conduit side Topic is what the translator's topic expression
	// evaluates to (Mapping.Topic), or "" when the translator supplies none.
	Topic, InputTopic string
	// Headers are the headers the chain added or changed (Kafka Connect: output
	// headers that differ from the input's; Conduit: the metadata keys the
	// translator maps to headers, Mapping.Headers).
	Headers map[string]any
	// ErrClass and ErrMessage describe the failure when Kind is ViewError: the
	// exception on the Kafka Connect side, ErrMessage alone (the error text)
	// on the Conduit side.
	ErrClass, ErrMessage string
}

// FromKafkaConnect builds the View of a captured Kafka Connect result for the
// given input record (the one the result is for: it supplies the input topic
// and the headers the chain did not add).
func FromKafkaConnect(res smtoracle.Result, in smtoracle.InputRecord) (View, error) {
	switch res.Result {
	case smtoracle.ResultDropped:
		return View{Kind: ViewDropped, InputTopic: in.Topic}, nil
	case smtoracle.ResultError:
		return View{Kind: ViewError, InputTopic: in.Topic, ErrClass: res.Error.Class, ErrMessage: res.Error.Message}, nil
	case smtoracle.ResultRecord:
	default:
		return View{}, fmt.Errorf("replay: unknown result %q", res.Result)
	}
	key, err := untag(res.Output.Key.Payload)
	if err != nil {
		return View{}, err
	}
	val, err := untag(res.Output.Value.Payload)
	if err != nil {
		return View{}, err
	}
	inHeaders := map[string]any{}
	for _, h := range in.Headers {
		hv, err := untag(h.Value)
		if err != nil {
			return View{}, err
		}
		inHeaders[h.Key] = hv
	}
	v := View{Kind: ViewRecord, Key: key, Value: val, Topic: res.Output.Topic, InputTopic: in.Topic, Headers: map[string]any{}}
	for _, h := range res.Output.Headers {
		hv, err := untag(h.Value)
		if err != nil {
			return View{}, err
		}
		if old, ok := inHeaders[h.Key]; ok && len(diffValues("", old, hv)) == 0 {
			continue // the chain did not touch it
		}
		v.Headers[h.Key] = hv
	}
	return v, nil
}

// Mapping is what a translator tells the harness about the parts of a record
// Conduit does not have: the topic its expression evaluates to, and which
// metadata keys carry the headers it writes.
type Mapping struct {
	// Topic is the evaluated topic expression. Leave it empty when the
	// translator emits none, in which case Compare fails if Kafka Connect
	// changed the topic.
	Topic string
	// Headers maps a Kafka Connect header name to the metadata key the
	// translator writes it to.
	Headers map[string]string
}

// FromConduit builds the View of one replay outcome, from the decoded record.
// The value is Payload.After, the key is Key; raw data is its base64 text, so
// a normalization that expects structured data fails loudly instead of
// comparing unlike things.
func FromConduit(o Outcome, m Mapping) (View, error) {
	switch {
	case o.Err != nil:
		return View{Kind: ViewError, ErrMessage: o.Err.Error()}, nil
	case o.Filtered:
		return View{Kind: ViewDropped}, nil
	}
	key, err := dataView(o.Decoded.Key)
	if err != nil {
		return View{}, err
	}
	val, err := dataView(o.Decoded.Payload.After)
	if err != nil {
		return View{}, err
	}
	v := View{Kind: ViewRecord, Key: key, Value: val, Topic: m.Topic, Headers: map[string]any{}}
	for name, mdKey := range m.Headers {
		if mv, ok := o.Decoded.Metadata[mdKey]; ok {
			v.Headers[name] = mv
		}
	}
	return v, nil
}

func dataView(d opencdc.Data) (any, error) {
	switch v := d.(type) {
	case nil:
		return nil, nil
	case opencdc.StructuredData:
		b, err := json.Marshal(map[string]any(v))
		if err != nil {
			return nil, err
		}
		return decodeNumbers(b)
	case opencdc.RawData:
		if v == nil {
			return nil, nil
		}
		return base64.StdEncoding.EncodeToString(v), nil
	}
	return nil, fmt.Errorf("replay: unsupported data %T", d)
}

func decodeNumbers(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// untag decodes a captured JSON payload and replaces the single-key tagged
// objects the oracle uses for Java types JSON cannot tell apart
// ({"$int32": 5}, {"$date": "..."}, {"$bytes": "..."}, ...) with the plain
// JSON value: a number, an RFC 3339 string, base64 text. This is the
// "types" normalization: Conduit widens ints and has no Date or Short, so
// the width and class of a Java type is a representation difference.
func untag(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	v, err := decodeNumbers(raw)
	if err != nil {
		return nil, err
	}
	return untagValue(v), nil
}

func untagValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 1 {
			for k, inner := range x {
				switch k {
				case "$int8", "$int16", "$int32", "$float32", "$decimal", "$double", "$date", "$bytes":
					return inner
				}
			}
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = untagValue(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = untagValue(e)
		}
		return out
	}
	return v
}

// Options select the normalizations applied to both sides before comparing.
// Each one covers a difference in how the two Postgres sources represent a
// column, never a difference an SMT makes. The zero value normalizes nothing
// beyond decoding.
type Options struct {
	// JSONFields are jsonb columns. Both sources write the JSON text as a
	// string (Conduit's connector before v0.21 wrote its base64). Both are
	// parsed, so key order and whitespace do not matter.
	JSONFields []string
	// DecimalFields are numeric columns, with their scale. Debezium writes a
	// number (the oracle's converter renders it as a decimal), Conduit a
	// fraction such as "999/100". Both become a decimal string of that scale.
	DecimalFields map[string]int
	// TimestampFields are timestamptz columns. Debezium writes microsecond
	// precision always ("...20.000000Z"), Conduit trims zeros ("...20Z").
	// Both become a UTC instant at microsecond precision.
	TimestampFields []string
	// ToastAbsent treats Debezium's unavailable-value placeholder for an
	// unchanged TOASTed column as the column being absent, which is how
	// Conduit reports it.
	ToastAbsent bool
	// ErrorMatch declares which Conduit error corresponds to which Kafka
	// Connect exception. A record both sides fail is only "the same" when an
	// entry matches the Kafka Connect failure and its Conduit text appears in
	// the Conduit error; an undeclared pair is reported with both texts, so a
	// chain that fails every record for the wrong reason cannot pass.
	ErrorMatch []ErrorMatch
}

// Normalize applies the options to a value (a Key or Value of a View).
func (o Options) Normalize(v any) (any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return v, nil
	}
	out := make(map[string]any, len(m))
	for k, e := range m {
		if o.ToastAbsent && e == ToastPlaceholder {
			continue
		}
		n, err := o.normalizeField(k, e)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", k, err)
		}
		out[k] = n
	}
	return out, nil
}

func (o Options) normalizeField(name string, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if contains(o.JSONFields, name) {
		return normalizeJSON(v)
	}
	if scale, ok := o.DecimalFields[name]; ok {
		return normalizeDecimal(v, scale)
	}
	if contains(o.TimestampFields, name) {
		return normalizeTimestamp(v)
	}
	return v, nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func normalizeJSON(v any) (any, error) {
	str, ok := v.(string)
	if !ok || str == "" {
		// Already structured, or empty text, which is not JSON: leave it so the comparison reports it.
		return v, nil
	}
	parsed, err := decodeNumbers([]byte(str))
	if err != nil {
		return nil, fmt.Errorf("not JSON text: %w", err)
	}
	return parsed, nil
}

func normalizeDecimal(v any, scale int) (any, error) {
	var s string
	switch x := v.(type) {
	case json.Number:
		s = x.String()
	case string:
		s = x
	default:
		return nil, fmt.Errorf("unsupported decimal %T", v)
	}
	r, ok := new(big.Rat).SetString(s) // accepts "12.50" and "999/100"
	if !ok {
		return nil, fmt.Errorf("not a decimal: %q", s)
	}
	return r.FloatString(scale), nil
}

func normalizeTimestamp(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("unsupported timestamp %T", v)
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil, err
	}
	return t.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z"), nil
}

// ErrorMatch pairs a Kafka Connect exception with the Conduit error that
// corresponds to it.
type ErrorMatch struct {
	// KCClass is the exception's class name, full or simple; it must match the
	// whole name (the simple name is what follows the last '.' or '$'), never
	// a suffix, and it is required.
	KCClass string
	// KCMessage, when set, must be contained in the exception's message.
	KCMessage string
	// Conduit must be contained in the Conduit error's text, for example a
	// stable error code. It is required: an empty entry would match every
	// failure.
	Conduit string
}

// Compare returns the differences between the Kafka Connect output and the
// Conduit output for one record, after the options' normalizations; an empty
// result means "diff clean". It compares the outcome (record, dropped or
// failed), the topic, the headers the chain added or changed, the key and the
// value. Each difference is one line naming the path.
//
// The topic fails closed: Kafka Connect rewriting it while the Conduit side
// supplies none (Mapping.Topic empty) is a difference.
func Compare(kc, conduit View, opts Options) ([]string, error) {
	if kc.Kind != conduit.Kind {
		return []string{fmt.Sprintf("outcome: Kafka Connect %s, Conduit %s", describeOutcome(kc), describeOutcome(conduit))}, nil
	}
	switch kc.Kind {
	case ViewDropped:
		return nil, nil
	case ViewError:
		return opts.compareErrors(kc, conduit), nil
	}
	var diffs []string
	switch {
	case conduit.Topic == "" && kc.Topic != kc.InputTopic:
		diffs = append(diffs, fmt.Sprintf("topic: Kafka Connect rewrote %q to %q, the Conduit side supplied no topic", kc.InputTopic, kc.Topic))
	case conduit.Topic != "" && conduit.Topic != kc.Topic:
		diffs = append(diffs, fmt.Sprintf("topic: Kafka Connect %q, Conduit %q", kc.Topic, conduit.Topic))
	}
	diffs = append(diffs, diffValues("headers", kc.Headers, conduit.Headers)...)
	for _, side := range []struct {
		name   string
		kc, cd any
	}{{"key", kc.Key, conduit.Key}, {"value", kc.Value, conduit.Value}} {
		a, err := opts.Normalize(side.kc)
		if err != nil {
			return nil, fmt.Errorf("replay: Kafka Connect %s: %w", side.name, err)
		}
		b, err := opts.Normalize(side.cd)
		if err != nil {
			return nil, fmt.Errorf("replay: Conduit %s: %w", side.name, err)
		}
		diffs = append(diffs, diffValues(side.name, a, b)...)
	}
	return diffs, nil
}

func describeOutcome(v View) string {
	if v.Kind == ViewError {
		return fmt.Sprintf("error (%s)", strings.TrimSpace(v.ErrClass+" "+v.ErrMessage))
	}
	return v.Kind
}

func (o Options) compareErrors(kc, conduit View) []string {
	for _, m := range o.ErrorMatch {
		if m.KCClass == "" || m.Conduit == "" {
			return []string{fmt.Sprintf("error: ErrorMatch %+v needs a KCClass and a Conduit text; an empty one would match every failure", m)}
		}
		if !classMatches(kc.ErrClass, m.KCClass) || !strings.Contains(kc.ErrMessage, m.KCMessage) {
			continue
		}
		if !strings.Contains(conduit.ErrMessage, m.Conduit) {
			return []string{fmt.Sprintf("error: Kafka Connect %s (%s), Conduit failed with %q, which does not contain %q", kc.ErrClass, describe(kc.ErrMessage), conduit.ErrMessage, m.Conduit)}
		}
		return nil
	}
	return []string{fmt.Sprintf("error: both sides fail, but no ErrorMatch pairs Kafka Connect %s (%s) with the Conduit error %q", kc.ErrClass, describe(kc.ErrMessage), conduit.ErrMessage)}
}

// classMatches reports whether want is the whole class name or its simple name.
func classMatches(class, want string) bool {
	if class == want {
		return true
	}
	i := strings.LastIndexAny(class, ".$")
	return i >= 0 && class[i+1:] == want
}

// CompareTombstone reports a difference when Kafka Connect's handling of the
// tombstone that follows a delete (smtoracle.Pair.Tombstone) is not what the
// translation does. Conduit emits no tombstone, so there is no Conduit record
// to compare: the translator states whether its chain keeps the tombstone
// (wantKept) and this checks that against Kafka Connect, which keeps it when
// the result is a record with a null value.
func CompareTombstone(kc View, wantKept bool) []string {
	kept := kc.Kind == ViewRecord && kc.Value == nil
	if kc.Kind == ViewError || kept == wantKept {
		if kc.Kind == ViewError {
			return []string{"tombstone: Kafka Connect failed it: " + describeOutcome(kc)}
		}
		return nil
	}
	return []string{fmt.Sprintf("tombstone: Kafka Connect kept=%v, the translation says kept=%v", kept, wantKept)}
}

func diffValues(path string, kc, cd any) []string {
	switch k := kc.(type) {
	case map[string]any:
		c, ok := cd.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: Kafka Connect an object, Conduit %s", path, describe(cd))}
		}
		keys := map[string]bool{}
		for n := range k {
			keys[n] = true
		}
		for n := range c {
			keys[n] = true
		}
		names := make([]string, 0, len(keys))
		for n := range keys {
			names = append(names, n)
		}
		sort.Strings(names)
		var out []string
		for _, n := range names {
			kv, kok := k[n]
			cv, cok := c[n]
			switch {
			case !kok:
				out = append(out, fmt.Sprintf("%s.%s: only in Conduit (%s)", path, n, describe(cv)))
			case !cok:
				out = append(out, fmt.Sprintf("%s.%s: only in Kafka Connect (%s)", path, n, describe(kv)))
			default:
				out = append(out, diffValues(path+"."+n, kv, cv)...)
			}
		}
		return out
	case []any:
		c, ok := cd.([]any)
		if !ok || len(c) != len(k) {
			return []string{fmt.Sprintf("%s: Kafka Connect %s, Conduit %s", path, describe(kc), describe(cd))}
		}
		var out []string
		for i := range k {
			out = append(out, diffValues(fmt.Sprintf("%s[%d]", path, i), k[i], c[i])...)
		}
		return out
	}
	if !equalScalar(kc, cd) {
		return []string{fmt.Sprintf("%s: Kafka Connect %s, Conduit %s", path, describe(kc), describe(cd))}
	}
	return nil
}

func equalScalar(a, b any) bool {
	an, aok := a.(json.Number)
	bn, bok := b.(json.Number)
	if aok && bok {
		ra, ok1 := new(big.Rat).SetString(an.String())
		rb, ok2 := new(big.Rat).SetString(bn.String())
		return ok1 && ok2 && ra.Cmp(rb) == 0
	}
	return a == b
}

func describe(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(b)
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return strings.ReplaceAll(s, "\n", " ")
}

// PostgresOptions returns the normalizations for a table of the fixture
// database (pg/01-schema.sql): its jsonb, numeric and timestamptz columns, and
// whether it has a TOASTed column. Translator tests use it so the columns that
// need a normalization are declared once, next to the schema that has them.
func PostgresOptions(table string) Options {
	const (
		attrs   = "attrs"
		price   = "price"
		created = "created"
	)
	switch table {
	case TableItems, TableStrict:
		return Options{
			JSONFields:      []string{attrs},
			DecimalFields:   map[string]int{price: 2},
			TimestampFields: []string{created},
			ToastAbsent:     true,
		}
	case TableToast:
		return Options{ToastAbsent: true}
	}
	return Options{}
}
