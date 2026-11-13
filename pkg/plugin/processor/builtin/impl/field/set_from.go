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

package field

import (
	"strings"

	"github.com/conduitio/conduit-commons/opencdc"
)

// The `from` parameter of field.set is read with a read-only walk instead of
// the SDK reference resolver. The resolver creates what it walks through (a
// nil .Payload.After becomes StructuredData{}, a missing parent becomes a
// map, #3147), and it cannot tell a missing field from a field that holds
// null. Kafka Connect tells them apart (a missing field throws, a null is
// copied), so field.set must too.
//
// The reference is first validated by sdk.NewReferenceResolver, so
// referenceFields only has to split a valid reference.
// TestReferenceFields_MatchesResolver checks that it walks the same path as
// the resolver.

// referenceFields splits a valid field reference into its field names, the
// way the SDK resolver does: `$` and lower-case root names are accepted, and
// a bracketed name is taken as written between its quotes.
func referenceFields(ref string) []string {
	ref = canonicalReference(ref)
	ref = strings.TrimPrefix(ref, "$")
	var fields []string
	for ref != "" {
		switch ref[0] {
		case '.':
			ref = ref[1:]
			end := strings.IndexAny(ref, ".[")
			if end < 0 {
				end = len(ref)
			}
			if end > 0 {
				fields = append(fields, ref[:end])
			}
			ref = ref[end:]
		case '[':
			// ["name"]: the SDK keeps escapes as written, so does this.
			i := 2
			for i < len(ref) && ref[i] != '"' {
				if ref[i] == '\\' {
					i++
				}
				i++
			}
			fields = append(fields, ref[2:i])
			ref = ref[min(i+2, len(ref)):] // skip `"]`
		default:
			return fields // unreachable for a validated reference
		}
	}
	return fields
}

// canonicalReference applies the SDK resolver's spelling rules for the root
// names (.payload, .key, ...).
func canonicalReference(ref string) string {
	for _, r := range [][2]string{
		{".payload", ".Payload"},
		{".key", ".Key"},
		{".metadata", ".Metadata"},
		{".position", ".Position"},
		{".operation", ".Operation"},
	} {
		if rest, ok := strings.CutPrefix(ref, r[0]); ok {
			ref = r[1] + rest
			break
		}
	}
	for _, r := range [][2]string{{".Payload.before", ".Payload.Before"}, {".Payload.after", ".Payload.After"}} {
		if rest, ok := strings.CutPrefix(ref, r[0]); ok {
			return r[1] + rest
		}
	}
	return ref
}

// lookupResult is the outcome of reading a field reference from a record.
type lookupResult int

const (
	// lookupFound means the field exists; its value may be nil.
	lookupFound lookupResult = iota
	// lookupMissing means a field on the path does not exist, or a parent
	// on the path is nil or not a map.
	lookupMissing
	// lookupRawSide means the path enters a key or payload side that holds
	// raw data, which has no fields.
	lookupRawSide
)

// lookup reads the field named by fields from rec without changing rec. The
// returned value is rec's own; the caller copies it.
func lookup(rec opencdc.Record, fields []string) (any, lookupResult) {
	if len(fields) == 0 {
		return nil, lookupMissing
	}
	var side opencdc.Data
	rest := fields[1:]
	switch fields[0] {
	case "Position":
		return rec.Position, okIfLast(rest)
	case "Operation":
		return rec.Operation, okIfLast(rest)
	case "Metadata":
		switch len(rest) {
		case 0:
			return rec.Metadata, lookupFound
		case 1:
			v, ok := rec.Metadata[rest[0]]
			if !ok {
				return nil, lookupMissing
			}
			return v, lookupFound
		default:
			return nil, lookupMissing
		}
	case "Key":
		side = rec.Key
	case "Payload":
		if len(rest) == 0 {
			return rec.Payload, lookupFound
		}
		switch rest[0] {
		case "Before":
			side = rec.Payload.Before
		case "After":
			side = rec.Payload.After
		default:
			return nil, lookupMissing
		}
		rest = rest[1:]
	default:
		return nil, lookupMissing
	}

	if len(rest) == 0 {
		return side, lookupFound // a whole side; nil is a value here
	}
	var cur any
	switch s := side.(type) {
	case nil:
		return nil, lookupMissing
	case opencdc.StructuredData:
		cur = map[string]any(s)
	default:
		return nil, lookupRawSide
	}
	for _, f := range rest {
		var m map[string]any
		switch c := cur.(type) {
		case map[string]any:
			m = c
		case opencdc.StructuredData:
			m = c
		default:
			return nil, lookupMissing // nil or a scalar parent has no fields
		}
		v, ok := m[f]
		if !ok {
			return nil, lookupMissing
		}
		cur = v
	}
	return cur, lookupFound
}

func okIfLast(rest []string) lookupResult {
	if len(rest) == 0 {
		return lookupFound
	}
	return lookupMissing
}

// deepCopy returns a copy of v that shares no maps, slices or bytes with it,
// keeping every type (opencdc's clone, applied to one value).
func deepCopy(v any) any {
	return opencdc.StructuredData{"v": v}.Clone().(opencdc.StructuredData)["v"] //nolint:forcetypeassert // Clone keeps the type
}
