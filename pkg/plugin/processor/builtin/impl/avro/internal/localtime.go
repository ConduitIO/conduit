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
	"strings"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
	"github.com/goccy/go-json"
)

// localWallClockChanged returns the name of the first top-level key of sd
// whose value is a time.Time in a local-timestamp-millis/micros field (at any
// depth) that the codec would write with a different wall clock than the
// value has, or "" if there is none.
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
// maps, unions). For a union it checks every branch the value could be, and
// a schema it cannot parse is not checked (the encode succeeded, so the
// schema is valid; only the extra check is skipped).
func localWallClockChanged(s schema.Schema, sd opencdc.StructuredData) string {
	var root any
	if err := json.Unmarshal(s.Bytes, &root); err != nil {
		return ""
	}
	w := &localWalk{named: map[string]any{}}
	w.collect(root, "")
	rec, ok := root.(map[string]any)
	if !ok {
		return ""
	}
	fields, _ := rec["fields"].([]any)
	byName := make(map[string]any, len(fields))
	for _, f := range fields {
		if fm, ok := f.(map[string]any); ok {
			if n, ok := fm["name"].(string); ok {
				byName[n] = fm["type"]
			}
		}
	}
	for _, k := range sortedKeys(sd) {
		if t, ok := byName[k]; ok && w.changed(t, sd[k], recNamespace(rec, "")) {
			return k
		}
	}
	return ""
}

type localWalk struct {
	named map[string]any // full and short names of record types
}

func recNamespace(rec map[string]any, parent string) string {
	if n, _ := rec["name"].(string); strings.Contains(n, ".") {
		return n[:strings.LastIndex(n, ".")]
	}
	if ns, ok := rec["namespace"].(string); ok {
		return ns
	}
	return parent
}

// collect registers every named record type in the schema JSON.
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
			ns = recNamespace(n, ns)
			short := name[strings.LastIndex(name, ".")+1:]
			w.named[short] = n
			if ns != "" {
				w.named[ns+"."+short] = n
			}
			fields, _ := n["fields"].([]any)
			for _, f := range fields {
				if fm, ok := f.(map[string]any); ok {
					w.collect(fm["type"], ns)
				}
			}
		case "array":
			w.collect(n["items"], ns)
		case "map":
			w.collect(n["values"], ns)
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
		if r, ok := w.named[t]; ok {
			return w.changed(r, v, ns)
		} else if r, ok := w.named[ns+"."+t]; ok {
			return w.changed(r, v, ns)
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
		return w.changedRecord(t, v, ns)
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

func (w *localWalk) changedRecord(t map[string]any, v any, ns string) bool {
	m, ok := asStringMap(v)
	if !ok {
		return false
	}
	ns = recNamespace(t, ns)
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
