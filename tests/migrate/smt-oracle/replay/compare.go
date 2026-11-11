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
	// Topic and Headers are only set on the Kafka Connect side, where a
	// record has them; Conduit has no counterpart, so they are not compared
	// unless the caller copies them over.
	Topic   string
	Headers map[string]any
}

// FromKafkaConnect builds the View of a captured Kafka Connect result. A
// result with an error, whatever the exception, is ViewError.
func FromKafkaConnect(res smtoracle.Result) (View, error) {
	switch res.Result {
	case smtoracle.ResultDropped:
		return View{Kind: ViewDropped}, nil
	case smtoracle.ResultError:
		return View{Kind: ViewError}, nil
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
	v := View{Kind: ViewRecord, Key: key, Value: val, Topic: res.Output.Topic, Headers: map[string]any{}}
	for _, h := range res.Output.Headers {
		hv, err := untag(h.Value)
		if err != nil {
			return View{}, err
		}
		v.Headers[h.Key] = hv
	}
	return v, nil
}

// FromConduit builds the View of one replay outcome, from the decoded record.
// The value is Payload.After, the key is Key; raw data is its base64 text, so a
// normalization that expects structured data fails loudly instead of
// comparing unlike things.
func FromConduit(o Outcome) (View, error) {
	switch {
	case o.Err != nil:
		return View{Kind: ViewError}, nil
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
	return View{Kind: ViewRecord, Key: key, Value: val}, nil
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
	// JSONFields are columns holding JSON. Debezium writes the JSON text as a
	// string; Conduit writes the bytes of the text, base64-encoded. Both are
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
	// NullAsAbsent treats a null field and a missing field as the same.
	NullAsAbsent bool
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
		if o.NullAsAbsent && e == nil {
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
	s, ok := v.(string)
	if !ok {
		return v, nil // already structured
	}
	// JSON text cannot be valid base64 (it contains braces, quotes or
	// spaces), so which of the two a string is, is unambiguous. A bare JSON
	// scalar such as 1 or true is the one exception and is read as text.
	if parsed, err := decodeNumbers([]byte(s)); err == nil {
		return parsed, nil
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("neither JSON text nor base64 of JSON text")
	}
	return decodeNumbers(raw)
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

// Compare returns the differences between the Kafka Connect output and the
// Conduit output for one record, after the options' normalizations; an empty
// result means "diff clean". Each difference is one line naming the path.
func Compare(kc, conduit View, opts Options) ([]string, error) {
	if kc.Kind != conduit.Kind {
		return []string{fmt.Sprintf("outcome: Kafka Connect %s, Conduit %s", kc.Kind, conduit.Kind)}, nil
	}
	if kc.Kind != ViewRecord {
		return nil, nil
	}
	var diffs []string
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
