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
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/errsafe"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/toschema"
	"github.com/goccy/go-json"
	"github.com/twmb/franz-go/pkg/sr"
)

// SchemaRef names the schema a record part carries: the subject and version
// in its schema metadata (opencdc.payload.schema.* or opencdc.key.schema.*).
// The zero value means the part carries no schema.
type SchemaRef struct {
	Subject string
	Version int
}

// IsZero reports whether r names no schema.
func (r SchemaRef) IsZero() bool { return r == SchemaRef{} }

// FromRecordSchemaStrategy encodes a record with the schema it already
// carries, instead of inferring one from its Go values.
//
// The processor SDK decodes the payload before avro.encode runs, and the
// decoded Go values no longer say whether a field was an Avro int or long, a
// date or a timestamp, or which precision and scale a decimal had. Inferring a
// schema from them (ExtractAndUploadSchemaStrategy) therefore changes the type
// signature even when no value changes. This strategy reads the carried schema
// from the registry, drops each top-level field the schema declares and the
// data no longer has (a processor removed it), adds a nullable field for each
// top-level field the data has and the schema lacks, and registers the result
// under Subject.
//
// A record that carries no schema is encoded with an inferred schema, as
// autoRegister would, with a warning logged once and
// measure.AvroEncodeSchemaFallbackCounter incremented for every such record.
//
// The Encoder verifies every encode made with this strategy by decoding it
// back (see Encoder.Encode), so a value that does not fit the schema exactly
// is an error (CodeSchemaMismatch), never a coerced or truncated value.
//
// It is safe for concurrent use. Use NewFromRecordSchemaStrategy.
type FromRecordSchemaStrategy struct {
	// Subject is the subject the encoding schema is registered under.
	Subject string

	fallbackOnce sync.Once
}

// NewFromRecordSchemaStrategy returns a strategy that registers its schemas
// under subject.
func NewFromRecordSchemaStrategy(subject string) *FromRecordSchemaStrategy {
	return &FromRecordSchemaStrategy{Subject: subject}
}

// GetSchema returns the schema sd is encoded with: the carried schema,
// without the top-level fields sd removed and with the ones it adds,
// registered under str.Subject. With a zero
// carried it falls back to inference.
func (str *FromRecordSchemaStrategy) GetSchema(
	ctx context.Context,
	registry schemaregistry.Registry,
	logger log.CtxLogger,
	sd opencdc.StructuredData,
	carried SchemaRef,
) (schema.Schema, error) {
	if carried.IsZero() {
		return str.fallback(ctx, registry, logger, sd, carried)
	}

	ss, err := registry.SchemaBySubjectVersion(ctx, carried.Subject, carried.Version)
	if err != nil {
		// Never fall back to inference here: a transient registry error would
		// otherwise register a lossy schema version under the subject.
		ce := conduiterr.Wrap(CodeRegistryUnavailable,
			fmt.Sprintf("could not get the schema the record carries (subject %q, version %d): %v", carried.Subject, carried.Version, err), err)
		ce.Suggestion = "check that the schema registry is reachable and still holds this subject and version"
		return schema.Schema{}, ce
	}
	if ss.Type != sr.TypeAvro {
		return schema.Schema{}, unsupported(fmt.Sprintf("the schema the record carries (subject %q, version %d) is %v, not Avro", carried.Subject, carried.Version, ss.Type))
	}
	if len(ss.References) > 0 {
		return schema.Schema{}, unsupported(fmt.Sprintf("the schema the record carries (subject %q, version %d) has schema references, which strategy fromRecord does not support", carried.Subject, carried.Version))
	}

	text, err := extendSchema(ss.Schema.Schema, sd)
	if err != nil {
		return schema.Schema{}, cerrors.Errorf("schema %q version %d: %w", carried.Subject, carried.Version, err)
	}

	registered, err := registry.CreateSchema(ctx, str.Subject, sr.Schema{Schema: text, Type: sr.TypeAvro})
	if err != nil {
		return schema.Schema{}, registerError(str.Subject, err)
	}
	return toschema.SrSubjectSchema(registered), nil
}

// fallback encodes a record that carries no schema with an inferred one.
func (str *FromRecordSchemaStrategy) fallback(
	ctx context.Context,
	registry schemaregistry.Registry,
	logger log.CtxLogger,
	sd opencdc.StructuredData,
	carried SchemaRef,
) (schema.Schema, error) {
	str.fallbackOnce.Do(func() {
		logger.Warn(ctx).
			Str("subject", str.Subject).
			Msg("record carries no schema for the encoded field, so schema strategy fromRecord infers one from the values, " +
				"which does not keep Avro int vs long, logical types or decimal precision and scale; " +
				"this is logged once, conduit_processor_avro_encode_schema_fallback_total counts every such record")
	})
	measure.AvroEncodeSchemaFallbackCounter.WithValues(str.Subject).Inc()
	//nolint:wrapcheck // same errors as strategy autoRegister
	return ExtractAndUploadSchemaStrategy{Subject: str.Subject}.GetSchema(ctx, registry, logger, sd, carried)
}

// extendSchema returns the Avro record schema text fitted to the top-level
// keys of sd: a declared field that sd does not have is dropped, and a
// nullable field is appended for each key of sd that the schema does not
// declare. It returns text unchanged when sd neither drops nor adds a field.
//
// Invariant 6: a field is dropped only when its key is absent from sd (a
// processor removed it), so no value is lost and none is invented: encoding
// with the field kept would write its default, or null, for data the record
// does not have. A key that is present is always encoded with its carried
// type, so a present value that does not fit (a different type, or null in a
// non-null field) is still an error from the codec or the round-trip check.
// Only top-level fields are dropped or added; inside a nested record the
// schema is used as declared.
func extendSchema(text string, sd opencdc.StructuredData) (string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &top); err != nil {
		return "", unsupported("the schema the record carries is not an Avro record")
	}
	var typ string
	if err := json.Unmarshal(top["type"], &typ); err != nil || typ != "record" {
		return "", unsupported("the schema the record carries is not an Avro record")
	}
	var fields []json.RawMessage
	if err := json.Unmarshal(top["fields"], &fields); err != nil {
		return "", unsupported("the schema the record carries has no readable field list")
	}

	declared := make(map[string]struct{}, len(fields))
	kept := make([]json.RawMessage, 0, len(fields))
	var dropped []string
	for _, f := range fields {
		var nf struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(f, &nf); err != nil || nf.Name == "" {
			return "", unsupported("the schema the record carries has a field without a name")
		}
		declared[nf.Name] = struct{}{}
		// Invariant 6: drop only on an absent key. A key present with a nil
		// value is data (null) and keeps its carried type.
		if _, ok := sd[nf.Name]; !ok {
			dropped = append(dropped, nf.Name)
			continue
		}
		kept = append(kept, f)
	}

	added := make([]string, 0)
	for k := range sd {
		if _, ok := declared[k]; !ok {
			added = append(added, k)
		}
	}
	if len(added) == 0 && len(dropped) == 0 {
		return text, nil
	}
	// Name order: the same added fields always give the same schema text, so
	// the registry returns the same ID instead of a new version per record.
	slices.Sort(added)

	newFields, err := inferAddedFields(sd, added)
	if err != nil {
		return "", err
	}
	kept = append(kept, newFields...)

	rawFields, err := json.Marshal(kept)
	if err != nil {
		return "", unsupported("could not build the extended schema")
	}
	top["fields"] = rawFields
	out, err := json.Marshal(top)
	if err != nil {
		return "", unsupported("could not build the extended schema")
	}

	// Fail closed if the result does not parse: an inferred record type can
	// reuse a name the carried schema defines, and a dropped field can hold
	// the definition of a named type (record, enum, fixed) that a kept field
	// refers to by name.
	if _, err := (schema.Schema{Type: schema.TypeAvro, Bytes: out}).Serde(); err != nil {
		switch {
		case len(dropped) > 0 && len(added) > 0:
			return "", unsupported(fmt.Sprintf("the schema the record carries cannot drop the fields %q the record removed and add the fields %q it added "+
				"(a removed field may define a named type another field uses, or an added field may reuse a name the schema defines)", dropped, added))
		case len(dropped) > 0:
			return "", unsupported(fmt.Sprintf("the schema the record carries cannot drop the fields %q the record removed: "+
				"one of them defines a named type another field uses", dropped))
		default:
			return "", unsupported(fmt.Sprintf("the fields %q the record adds cannot be added to the schema it carries", added))
		}
	}
	return string(out), nil
}

// inferAddedFields returns one Avro field for each name in added, typed by
// inference from its value in sd, nullable and defaulting to null.
//
// Invariant 6: nullable with a null default keeps the extended schema
// backward compatible with the carried one (a reader with the extended schema
// can read data written with the carried schema), and matches what Debezium's
// ExtractNewRecordState does for the fields it adds.
func inferAddedFields(sd opencdc.StructuredData, added []string) ([]json.RawMessage, error) {
	sub := make(opencdc.StructuredData, len(added))
	for _, k := range added {
		sub[k] = sd[k]
	}
	inferred, err := schema.KnownSerdeFactories[schema.TypeAvro].SerdeForType(sub)
	if err != nil {
		return nil, unsupported(fmt.Sprintf("could not infer a type for the fields %q the record adds: %v", added, errsafe.Opaque(err)))
	}
	var rec struct {
		Fields []struct {
			Name string          `json:"name"`
			Type json.RawMessage `json:"type"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(inferred.String()), &rec); err != nil {
		return nil, unsupported("could not read the inferred type of the added fields")
	}
	byName := make(map[string]json.RawMessage, len(rec.Fields))
	for _, f := range rec.Fields {
		byName[f.Name] = f.Type
	}

	out := make([]json.RawMessage, 0, len(added))
	for _, name := range added {
		t, ok := byName[name]
		if !ok {
			return nil, unsupported(fmt.Sprintf("could not infer a type for the field %q the record adds", name))
		}
		nt, err := nullable(t)
		if err != nil {
			return nil, err
		}
		f, err := json.Marshal(struct {
			Name    string          `json:"name"`
			Type    json.RawMessage `json:"type"`
			Default *struct{}       `json:"default"` // always null
		}{Name: name, Type: nt})
		if err != nil {
			return nil, unsupported("could not build the extended schema")
		}
		out = append(out, f)
	}
	return out, nil
}

// nullable returns t as a union whose first branch is null, so that a null
// default is valid for it.
func nullable(t json.RawMessage) (json.RawMessage, error) {
	var branches []json.RawMessage
	if err := json.Unmarshal(t, &branches); err != nil {
		branches = []json.RawMessage{t} // not a union
	}
	out := []json.RawMessage{json.RawMessage(`"null"`)}
	for _, b := range branches {
		var s string
		if json.Unmarshal(b, &s) == nil && s == "null" {
			continue
		}
		out = append(out, b)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, unsupported("could not build the extended schema")
	}
	return b, nil
}

// registerError classifies a failure to register the encoding schema.
func registerError(subject string, err error) error {
	var re *sr.ResponseError
	if cerrors.As(err, &re) && re.StatusCode == http.StatusConflict {
		ce := conduiterr.Wrap(CodeSchemaIncompatible,
			fmt.Sprintf("the schema registry refused the encoding schema for subject %q under its compatibility rule: %v", subject, err), err)
		ce.ConfigPath = "schema.fromRecord.subject"
		ce.Suggestion = "the subject holds versions this schema is not compatible with (for example earlier autoRegister versions); " +
			"use a new subject, or change the compatibility setting of this subject deliberately"
		return ce
	}
	ce := conduiterr.Wrap(CodeRegistryUnavailable,
		fmt.Sprintf("could not register the encoding schema for subject %q: %v", subject, err), err)
	ce.Suggestion = "check that the schema registry is reachable"
	return ce
}

func unsupported(msg string) error {
	ce := conduiterr.New(CodeSchemaUnsupported, msg)
	ce.ConfigPath = "schema.strategy"
	ce.Suggestion = "strategy fromRecord needs records whose schema metadata names an Avro record schema without references; " +
		"use strategy preRegistered for other records"
	return ce
}

// mismatch reports a value that does not fit the encoding schema. It names
// the top-level field, never the value: errors end up in logs and the DLQ.
// cause, when not nil, is kept for errors.Is/As but not shown.
func mismatch(field, what string, cause error) error {
	msg := fmt.Sprintf("field %q %s", field, what)
	ce := conduiterr.New(CodeSchemaMismatch, msg)
	if cause != nil {
		ce = conduiterr.Wrap(CodeSchemaMismatch, msg, errsafe.Opaque(cause))
	}
	ce.Suggestion = "the value no longer fits the schema the record carries and is not changed to fit it; " +
		"fix the processor that changed it, or attach a schema that describes it"
	return ce
}
