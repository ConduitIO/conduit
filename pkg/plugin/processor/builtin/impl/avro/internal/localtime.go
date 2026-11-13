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

package internal

import (
	"reflect"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
	"github.com/iskorotkov/avro/v2"
)

// localTimeCheck finds values in local-timestamp-millis/micros/nanos fields
// that the codec would write with a different wall clock than the value has.
// The zero value is ready to use and safe for concurrent use.
//
// Invariant 6: a local-timestamp field holds a wall clock with no zone. The
// codec writes the wall clock the instant has in time.Local, not the wall
// clock of the time.Time's own location, and reads it back as the same
// instant, so the round trip (which compares instants) cannot see the
// difference. A reader of the Avro value sees the Local wall clock. A value
// whose location offset differs from time.Local's at that instant would
// change its wall clock, so it is reported as a mismatch.
//
// The check walks the codec's own parse of the schema text, not the JSON, so
// names, aliases, namespaces, wrapped types, escapes and error types resolve
// exactly as they do when the record is encoded. For a union it checks every
// branch the value could be, so a union that lists both a timestamp-* and a
// local-timestamp-* branch is checked as local-timestamp (fail closed).
//
// Cost: the parse is cached by schema text (see localTimeCheck.entry), along
// with the top-level fields that can hold a local-timestamp value at any
// depth. A schema with none costs one map lookup per record; only those
// fields are walked otherwise.
type localTimeCheck struct {
	mu      sync.Mutex
	entries map[string]*localSchema // by schema text
	bytes   int                     // total length of the cached texts
}

// Cache bounds. Each distinct schema version a processor encodes with is one
// entry. A text above maxLocalSchemaBytes is parsed for each record and not
// kept; when the cache reaches maxLocalSchemas entries or maxLocalCacheBytes
// of text it is cleared and refilled, which only costs a parse per schema
// again. A parsed schema takes memory roughly in proportion to its text.
const (
	maxLocalSchemas     = 256
	maxLocalSchemaBytes = 256 << 10
	maxLocalCacheBytes  = 8 << 20
)

// localSchema is the parse of one schema text. It is read-only once built, so
// one value serves concurrent records.
type localSchema struct {
	// fields are the top-level fields whose type contains a local-timestamp
	// type at any depth; empty when there is nothing to check.
	fields []*avro.Field
	// failed is set when the codec cannot parse the text, or it is not a
	// record. The encode with the same text succeeded, so this cannot happen;
	// if it does, the record is refused rather than not checked.
	failed bool
}

// changed returns the name of the first top-level key of sd, in schema field
// order, whose value holds a time.Time in a local-timestamp field (at any
// depth) that would change wall clock, "(record)" if the schema cannot be
// checked, or "" if there is none.
func (c *localTimeCheck) changed(s schema.Schema, sd opencdc.StructuredData) string {
	ls := c.entry(s.Bytes)
	if ls.failed {
		return "(record)"
	}
	for _, f := range ls.fields {
		if v, ok := sd[f.Name()]; ok && localChanged(f.Type(), v) {
			return f.Name()
		}
	}
	return ""
}

// entry returns the parse of text, from the cache when it is there.
func (c *localTimeCheck) entry(text []byte) *localSchema {
	c.mu.Lock()
	ls, ok := c.entries[string(text)]
	c.mu.Unlock()
	if ok {
		return ls
	}
	ls = parseLocalSchema(text)
	if len(text) > maxLocalSchemaBytes {
		return ls
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[string(text)]; ok {
		return ls // another record stored it meanwhile
	}
	if c.entries == nil || len(c.entries) >= maxLocalSchemas || c.bytes+len(text) > maxLocalCacheBytes {
		c.entries = make(map[string]*localSchema)
		c.bytes = 0
	}
	c.entries[string(text)] = ls
	c.bytes += len(text)
	return ls
}

func parseLocalSchema(text []byte) *localSchema {
	parsed, err := avro.ParseBytesWithCache(text, "", &avro.SchemaCache{}) //nolint:forbidigo // a fresh cache owned by this one schema, as conduit-commons schema/avro.Parse does
	if err != nil {
		return &localSchema{failed: true}
	}
	rec, ok := parsed.(*avro.RecordSchema)
	if !ok {
		return &localSchema{failed: true}
	}
	ls := &localSchema{}
	for _, f := range rec.Fields() {
		if hasLocal(f.Type(), map[string]bool{}) {
			ls.fields = append(ls.fields, f)
		}
	}
	return ls
}

func isLocalTimestamp(s *avro.PrimitiveSchema) bool {
	l := s.Logical()
	if l == nil {
		return false
	}
	lt := l.Type()
	return lt == avro.LocalTimestampMillis || lt == avro.LocalTimestampMicros || lt == avro.LocalTimestampNanos
}

// hasLocal reports whether a value of type s can hold a local-timestamp value.
// seen holds the records already being visited, so a recursive type ends.
func hasLocal(s avro.Schema, seen map[string]bool) bool {
	switch t := s.(type) {
	case *avro.PrimitiveSchema:
		return isLocalTimestamp(t)
	case *avro.RefSchema:
		return hasLocal(t.Schema(), seen)
	case *avro.RecordSchema:
		if seen[t.FullName()] {
			return false
		}
		seen[t.FullName()] = true
		for _, f := range t.Fields() {
			if hasLocal(f.Type(), seen) {
				return true
			}
		}
	case *avro.UnionSchema:
		for _, b := range t.Types() {
			if hasLocal(b, seen) {
				return true
			}
		}
	case *avro.ArraySchema:
		return hasLocal(t.Items(), seen)
	case *avro.MapSchema:
		return hasLocal(t.Values(), seen)
	}
	return false
}

// localChanged reports whether v, in a position of Avro type s, holds a time
// in a local-timestamp field that would change wall clock.
func localChanged(s avro.Schema, v any) bool {
	if isNil(v) {
		return false
	}
	switch t := s.(type) {
	case *avro.PrimitiveSchema:
		tm, ok := v.(time.Time)
		return ok && isLocalTimestamp(t) && wallClockChanges(tm)
	case *avro.RefSchema:
		return localChanged(t.Schema(), v)
	case *avro.RecordSchema:
		m, ok := asStringMap(v)
		if !ok {
			return false
		}
		for _, f := range t.Fields() {
			if fv, ok := m[f.Name()]; ok && localChanged(f.Type(), fv) {
				return true
			}
		}
	case *avro.UnionSchema:
		for _, b := range t.Types() {
			if localChanged(b, v) {
				return true
			}
		}
	case *avro.ArraySchema:
		return localChangedItems(t.Items(), v)
	case *avro.MapSchema:
		return localChangedValues(t.Values(), v)
	}
	return false
}

// wallClockChanges reports whether the codec, which writes the wall clock tm
// has in time.Local, would write a different wall clock than tm's own: true
// when tm's zone offset differs from time.Local's at that instant.
func wallClockChanges(tm time.Time) bool {
	_, own := tm.Zone()
	_, local := tm.In(time.Local).Zone()
	return own != local
}

func localChangedItems(items avro.Schema, v any) bool {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return false
	}
	for i := range rv.Len() {
		if localChanged(items, rv.Index(i).Interface()) {
			return true
		}
	}
	return false
}

func localChangedValues(values avro.Schema, v any) bool {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
		return false
	}
	iter := rv.MapRange()
	for iter.Next() {
		if localChanged(values, iter.Value().Interface()) {
			return true
		}
	}
	return false
}
