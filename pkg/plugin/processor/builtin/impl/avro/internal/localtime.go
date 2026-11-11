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
	"bytes"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
	"github.com/goccy/go-json"
)

// localTimeCheck finds values in local-timestamp-millis/micros fields that the
// codec would write with a different wall clock than the value has. The zero
// value is ready to use and safe for concurrent use.
//
// Invariant 6: a local-timestamp field holds a wall clock with no zone. The
// codec writes the wall clock the instant has in time.Local, not the wall
// clock of the time.Time's own location, and reads it back as the same
// instant, so the round trip (which compares instants) cannot see the
// difference. A reader of the Avro value sees the Local wall clock. A value
// whose location offset differs from time.Local's at that instant would
// change its wall clock, so it is reported as a mismatch.
//
// Limit: the walk follows the schema JSON (records, named references, arrays,
// maps, unions). For a union it checks every branch the value could be, so a
// union that lists both a timestamp-* and a local-timestamp-* branch is
// checked as local-timestamp (fail closed). A schema it cannot parse is not
// checked (the encode succeeded, so the schema is valid; only the extra check
// is skipped).
//
// Cost: a schema whose text has no "local-timestamp" is skipped with a byte
// search. Otherwise the parsed walk is cached by schema text, so the schema
// JSON is parsed once per schema, not once per record.
type localTimeCheck struct {
	mu    sync.Mutex
	walks map[string]*localWalk // by schema text; nil when there is nothing to check
}

// maxLocalWalks bounds the cache. Each distinct schema version a processor
// encodes with is one entry; when it is full the cache is cleared and refilled,
// which only costs a parse per schema again.
const maxLocalWalks = 256

var localTimestampMarker = []byte("local-timestamp")

// changed returns the name of the first top-level key of sd, in name order,
// whose value holds a time.Time in a local-timestamp field (at any depth)
// that would change wall clock, or "" if there is none.
func (c *localTimeCheck) changed(s schema.Schema, sd opencdc.StructuredData) string {
	if !bytes.Contains(s.Bytes, localTimestampMarker) {
		return ""
	}
	w := c.walk(s.Bytes)
	if w == nil {
		return ""
	}
	for _, k := range sortedKeys(sd) {
		if t, ok := w.fields[k]; ok && w.changed(t, sd[k], w.ns) {
			return k
		}
	}
	return ""
}

func (c *localTimeCheck) walk(text []byte) *localWalk {
	c.mu.Lock()
	w, ok := c.walks[string(text)]
	c.mu.Unlock()
	if ok {
		return w
	}
	w = newLocalWalk(text)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.walks == nil || len(c.walks) >= maxLocalWalks {
		c.walks = make(map[string]*localWalk)
	}
	c.walks[string(text)] = w
	return w
}

// localWalk is the parsed schema of one record type. It is read-only after
// newLocalWalk returns, so one walk serves concurrent records.
type localWalk struct {
	named  map[string]namedRecord // record types by full name
	fields map[string]any         // top-level field name -> Avro type
	ns     string                 // namespace of the top-level record
}

// namedRecord is a record type and the namespace bare names inside it
// resolve in, which is where it is defined, not where it is referenced.
type namedRecord struct {
	node map[string]any
	ns   string
}

// newLocalWalk parses text, or returns nil when it is not a record schema.
func newLocalWalk(text []byte) *localWalk {
	var root any
	if err := json.Unmarshal(text, &root); err != nil {
		return nil
	}
	rec, ok := root.(map[string]any)
	if !ok {
		return nil
	}
	w := &localWalk{named: map[string]namedRecord{}, ns: recNamespace(rec, "")}
	w.collect(root, "")
	fields, _ := rec["fields"].([]any)
	w.fields = make(map[string]any, len(fields))
	for _, f := range fields {
		if fm, ok := f.(map[string]any); ok {
			if n, ok := fm["name"].(string); ok {
				w.fields[n] = fm["type"]
			}
		}
	}
	return w
}

// recNamespace returns the namespace a record defines for the names inside
// it: the prefix of a dotted name, else its namespace attribute ("" is the
// null namespace), else the enclosing namespace parent.
func recNamespace(rec map[string]any, parent string) string {
	if n, _ := rec["name"].(string); strings.Contains(n, ".") {
		return n[:strings.LastIndex(n, ".")]
	}
	if ns, ok := rec["namespace"].(string); ok {
		return ns
	}
	return parent
}

// fullName resolves an Avro type name as written at a place whose enclosing
// namespace is ns: a name with a dot is already a full name, a bare name is
// qualified with ns, and in the null namespace a bare name is its own full
// name.
func fullName(ns, name string) string {
	if ns == "" || strings.Contains(name, ".") {
		return name
	}
	return ns + "." + name
}

// collect registers every named record type in the schema JSON by its full
// name. ns is the namespace enclosing node.
func (w *localWalk) collect(node any, ns string) {
	switch n := node.(type) {
	case []any:
		for _, b := range n {
			w.collect(b, ns)
		}
	case map[string]any:
		switch n["type"] {
		case "record":
			name, _ := n["name"].(string)
			inner := recNamespace(n, ns)
			w.named[fullName(inner, name[strings.LastIndex(name, ".")+1:])] = namedRecord{node: n, ns: inner}
			fields, _ := n["fields"].([]any)
			for _, f := range fields {
				if fm, ok := f.(map[string]any); ok {
					w.collect(fm["type"], inner)
				}
			}
		case "array":
			w.collect(n["items"], ns)
		case "map":
			w.collect(n["values"], ns)
		default:
			// {"type": {...}} wraps a type definition
			if nested, ok := n["type"].(map[string]any); ok {
				w.collect(nested, ns)
			}
		}
	}
}

// changed reports whether v, in a position of Avro type typ, holds a time in
// a local-timestamp field that would change wall clock.
func (w *localWalk) changed(typ, v any, ns string) bool {
	if isNil(v) {
		return false
	}
	switch t := typ.(type) {
	case string:
		// A reference to a named type. Primitive names are not in w.named.
		if r, ok := w.named[fullName(ns, t)]; ok {
			return w.changedRecord(r.node, v, r.ns)
		}
	case []any:
		for _, b := range t {
			if w.changed(b, v, ns) {
				return true
			}
		}
	case map[string]any:
		return w.changedNode(t, v, ns)
	}
	return false
}

func (w *localWalk) changedNode(t map[string]any, v any, ns string) bool {
	if lt, _ := t["logicalType"].(string); lt == "local-timestamp-millis" || lt == "local-timestamp-micros" {
		tm, ok := v.(time.Time)
		if !ok {
			return false
		}
		_, own := tm.Zone()
		_, local := tm.In(time.Local).Zone()
		return own != local
	}
	switch t["type"] {
	case "record":
		return w.changedRecord(t, v, recNamespace(t, ns))
	case "array":
		return w.changedItems(t["items"], v, ns)
	case "map":
		return w.changedValues(t["values"], v, ns)
	case "string", "int", "long", "float", "double", "boolean", "bytes", "null":
		// primitives and logical types other than local-timestamp: nothing to check
		return false
	}
	if nested, ok := t["type"].(map[string]any); ok {
		return w.changedNode(nested, v, ns)
	}
	return false
}

// changedRecord checks the fields of record type t present in v. ns is the
// namespace t defines for the names inside it.
func (w *localWalk) changedRecord(t map[string]any, v any, ns string) bool {
	m, ok := asStringMap(v)
	if !ok {
		return false
	}
	fields, _ := t["fields"].([]any)
	for _, f := range fields {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		name, _ := fm["name"].(string)
		if fv, ok := m[name]; ok && w.changed(fm["type"], fv, ns) {
			return true
		}
	}
	return false
}

func (w *localWalk) changedItems(items, v any, ns string) bool {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return false
	}
	for i := range rv.Len() {
		if w.changed(items, rv.Index(i).Interface(), ns) {
			return true
		}
	}
	return false
}

func (w *localWalk) changedValues(values, v any, ns string) bool {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
		return false
	}
	iter := rv.MapRange()
	for iter.Next() {
		if w.changed(values, iter.Value().Interface(), ns) {
			return true
		}
	}
	return false
}
