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

//go:generate paramgen -output=flatten_paramgen.go flattenConfig

// Package flatten implements the `flatten` built-in processor, the Conduit
// equivalent of the Apache Kafka Connect Flatten SMT
// (org.apache.kafka.connect.transforms.Flatten$Key and $Value).
//
// It replaces a structured key, before-image or after-image with a map of one
// level: every leaf is stored under the names of the fields on its path
// joined by a delimiter. Nested maps (map[string]any and
// opencdc.StructuredData) are flattened; every other value, arrays included,
// is a leaf and kept as it is. An empty nested map produces no key, as in
// Kafka Connect.
//
// Invariants:
//
//   - A value is never overwritten. When two paths flatten to the same key
//     (`{"a.b": 1, "a": {"b": 2}}` with "."), the record fails with
//     flatten.key_collision, whatever the map iteration order (invariant 6).
//   - A nil side passes through unchanged (a delete's after-image, a
//     tombstone), as Kafka Connect passes a null value through.
//   - The schema subject and version of a flattened side are removed, so the
//     SDK schema middleware does not register a schema extracted from the
//     flat values as a new version of the source's subject.
//
// Null and missing fields: a null leaf is kept as a field with a null value,
// as in Kafka Connect's schemaless mode. A null nested map is a null leaf
// too (`{"a": nil}` stays `{"a": nil}`); Kafka Connect in schema mode instead
// emits a null field per field of the struct's schema, which Conduit cannot
// know without the schema. Leaves of any other type (time.Time, *big.Rat)
// are kept as they are, where Kafka Connect's schemaless mode rejects types
// that are not Connect types. The SMT translator accounts for both.
package flatten

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

const (
	fieldKey    = ".Key"
	fieldBefore = ".Payload.Before"
	fieldAfter  = ".Payload.After"
)

// Processor is the `flatten` built-in processor. See the package
// documentation for its behaviour.
type Processor struct {
	sdk.UnimplementedProcessor

	field     string
	delimiter string
}

// NewProcessor returns an unconfigured `flatten` processor.
func NewProcessor(log.CtxLogger) *Processor { return &Processor{} }

type flattenConfig struct {
	// Field is the side of the record to flatten: `.Key`, `.Payload.Before` or
	// `.Payload.After`. The default `.Payload.After` matches Kafka Connect's
	// `Flatten$Value`; `.Key` matches `Flatten$Key`.
	Field string `json:"field" default:".Payload.After" validate:"inclusion=.Key|.Payload.Before|.Payload.After"`
	// Delimiter is inserted between the field names of each level when the
	// flattened names are built. The default is `.`, as in Kafka Connect. An
	// empty value is replaced by the default, so an empty delimiter (which
	// Kafka Connect accepts) cannot be configured.
	Delimiter string `json:"delimiter" default:"."`
}

func (p *Processor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "flatten",
		Summary: "Flatten a nested structured key or payload into one level.",
		Description: `Flatten replaces the structured data in ` + "`field`" + ` (` + "`.Key`" + `, ` + "`.Payload.Before`" + ` or
` + "`.Payload.After`" + `) with a map of one level. Each leaf is stored under the names of the fields on its path,
joined by ` + "`delimiter`" + `: ` + "`{\"a\": {\"b\": 1}}`" + ` becomes ` + "`{\"a.b\": 1}`" + `. It is the equivalent of the
Kafka Connect ` + "`Flatten$Value`" + ` and ` + "`Flatten$Key`" + ` single message transforms.

Arrays are not flattened: they are kept as they are, like every other value that is not a map. An empty nested map
produces no field. A null field, including a null nested map, is kept as a field with a null value.

An empty ` + "`delimiter`" + ` is replaced by the default ` + "`.`" + `, so a Kafka Connect ` + "`Flatten`" + ` with ` + "`delimiter=`" + `
(empty) has no equivalent here.

If two paths produce the same name (` + "`{\"a.b\": 1, \"a\": {\"b\": 2}}`" + ` with the delimiter ` + "`.`" + `), the record fails
with the error code ` + "`flatten.key_collision`" + `, naming both paths and the name. A value is never overwritten.
Kafka Connect overwrites one value with the other in this case when the record has no schema, and fails when it has
one.

A nil key or payload (a delete's after-image, a tombstone) passes through unchanged. Raw data fails with
` + "`flatten.not_structured`" + `; decode it first, for example with
[` + "`json.decode`" + `](/docs/using/processors/builtin/json.decode).

The processor removes the schema subject and version of the side it flattens (` + "`opencdc.key.schema.*`" + ` for the
key, ` + "`opencdc.payload.schema.*`" + ` for the payload), because the flat data no longer matches the schema the source
attached. The record then leaves the processor as structured data without a schema.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: flattenConfig{}.Parameters(),
	}, nil
}

func (p *Processor) Configure(ctx context.Context, c config.Config) error {
	cfg := flattenConfig{}
	if err := sdk.ParseConfig(ctx, c, &cfg, flattenConfig{}.Parameters()); err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}
	p.field = cfg.Field
	p.delimiter = cfg.Delimiter
	return nil
}

func (p *Processor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		if err := p.flattenRecord(&rec); err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, sdk.SingleRecord(rec))
	}
	return out
}

func (p *Processor) flattenRecord(rec *opencdc.Record) error {
	var side *opencdc.Data
	switch p.field {
	case fieldKey:
		side = &rec.Key
	case fieldBefore:
		side = &rec.Payload.Before
	default:
		side = &rec.Payload.After
	}

	switch data := (*side).(type) {
	case nil:
		// A nil side is a delete's after-image or a tombstone: it passes
		// through, as in Kafka Connect.
		return nil
	case opencdc.StructuredData:
		if data == nil {
			// A typed nil is no data either: pass it through as nil, with
			// its schema keys.
			*side = nil
			return nil
		}
		flat, err := flatten(data, p.delimiter, p.field)
		if err != nil {
			return err
		}
		*side = flat
		// Invariant 6: the flat side no longer matches the schema the source
		// attached; without these keys the SDK schema middleware leaves it
		// structured instead of registering a lossy extracted schema.
		if p.field == fieldKey {
			delete(rec.Metadata, opencdc.MetadataKeySchemaSubject)
			delete(rec.Metadata, opencdc.MetadataKeySchemaVersion)
		} else {
			delete(rec.Metadata, opencdc.MetadataPayloadSchemaSubject)
			delete(rec.Metadata, opencdc.MetadataPayloadSchemaVersion)
		}
		return nil
	default:
		e := conduiterr.New(CodeNotStructured, fmt.Sprintf("%s does not contain structured data", p.field))
		e.ConfigPath = "settings.field"
		e.Suggestion = "Run flatten only on structured data; decode raw data first, for example with json.decode."
		return e
	}
}

// flatten returns data with nested maps flattened into one level. root is
// the reference of data (for example .Payload.After), used to name paths in
// a collision error. Keys are visited in sorted order, so the same input
// always reports the same collision.
func flatten(data opencdc.StructuredData, delimiter, root string) (opencdc.StructuredData, error) {
	out := make(opencdc.StructuredData, len(data))
	// paths maps each output key to the path that produced it, to name both
	// paths of a collision.
	paths := make(map[string][]string, len(data))

	var walk func(prefix string, path []string, m map[string]any) error
	walk = func(prefix string, path []string, m map[string]any) error {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			name := k
			if len(path) > 0 {
				name = prefix + delimiter + k
			}
			fieldPath := append(slices.Clone(path), k)
			// A nested map is flattened; a nil one (typed or not) is a
			// null leaf and kept, never dropped.
			switch v := m[k].(type) {
			case map[string]any:
				if v != nil {
					if err := walk(name, fieldPath, v); err != nil {
						return err
					}
					continue
				}
			case opencdc.StructuredData:
				if v != nil {
					if err := walk(name, fieldPath, v); err != nil {
						return err
					}
					continue
				}
			}
			if prev, ok := paths[name]; ok {
				return collisionError(root, name, prev, fieldPath)
			}
			paths[name] = fieldPath
			out[name] = m[k]
		}
		return nil
	}
	if err := walk("", nil, data); err != nil {
		return nil, err
	}
	return out, nil
}

func collisionError(root, name string, a, b []string) error {
	pa, pb := reference(root, a), reference(root, b)
	if pb < pa {
		pa, pb = pb, pa
	}
	e := conduiterr.New(CodeKeyCollision,
		fmt.Sprintf("%s and %s both flatten to the field name %s", pa, pb, strconv.Quote(name)))
	e.ConfigPath = "settings.delimiter"
	e.Suggestion = "Use a delimiter that does not occur in the field names, or rename one of the fields before flatten."
	return e
}

// reference renders a field path in the reference syntax of the processors,
// quoting a name that is not a plain identifier: .Payload.After.a["b.c"].
func reference(root string, path []string) string {
	var b strings.Builder
	b.WriteString(root)
	for _, f := range path {
		if isIdentifier(f) {
			b.WriteString(".")
			b.WriteString(f)
			continue
		}
		b.WriteString("[")
		b.WriteString(strconv.Quote(f))
		b.WriteString("]")
	}
	return b.String()
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 && unicode.IsDigit(r) {
			return false
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
