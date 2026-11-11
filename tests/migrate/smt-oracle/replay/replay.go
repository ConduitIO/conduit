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

// Package replay runs a Conduit processor chain over the OpenCDC records the
// SMT oracle captured from Conduit's Postgres source, with no Docker and no
// network, so a translator PR can prove in the ordinary `test` job that what
// it emits does what Kafka Connect's SMT did.
//
// The chain runs through the same code the engine uses: processors come from
// the built-in registry, wrapped in processor.RunnableProcessor (so the
// condition, the schema middleware and the error and filter results behave as
// in a pipeline), and schema lookups go to an in-memory schema registry loaded
// with the schemas behind the captured records. A record is never acked or
// delivered; the harness only observes what the chain returns.
package replay

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"time"

	"github.com/goccy/go-json"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	commonsschema "github.com/conduitio/conduit-commons/schema"
	sdk "github.com/conduitio/conduit-processor-sdk"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/foundation/log"
	proc_plugin "github.com/conduitio/conduit/pkg/plugin/processor"
	proc_builtin "github.com/conduitio/conduit/pkg/plugin/processor/builtin"
	"github.com/conduitio/conduit/pkg/processor"
	smtoracle "github.com/conduitio/conduit/tests/migrate/smt-oracle"
	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/sr"
)

// Step is one processor of the chain, as the translator emits it: the plugin,
// its settings and its optional condition (a Go template, as in a pipeline).
type Step struct {
	Plugin    string
	Settings  map[string]string
	Condition string
}

// Outcome is what the chain did to one record.
type Outcome struct {
	// Record is the record after the last processor that ran, as the engine
	// would pass it on: a record that carries a schema comes back as raw
	// bytes in that schema's format. It is the input record when Filtered or
	// Err is set.
	Record opencdc.Record
	// Decoded is Record with every raw side that has schema metadata decoded
	// through the registry into structured data, which is what Compare reads.
	// It equals Record when there is nothing to decode.
	Decoded opencdc.Record
	// Filtered is true when a processor dropped the record (as a Filter does).
	Filtered bool
	// Err is set when a processor failed the record. In a pipeline this is
	// what would send the record to the DLQ or stop the pipeline.
	Err error
}

// Replayer holds the in-memory schema registry. Create one per test.
type Replayer struct {
	logger   log.CtxLogger
	registry *schemaregistry.SchemaRegistry
	service  *processor.Service
}

// New creates a Replayer whose schema registry holds schemas, registered in
// the given order so every subject keeps the version it had in the capture.
// It fails if a version would differ.
func New(ctx context.Context, schemas []smtoracle.Schema) (*Replayer, error) {
	db := &inmemory.DB{}
	reg, err := schemaregistry.NewSchemaRegistry(db)
	if err != nil {
		return nil, fmt.Errorf("replay: schema registry: %w", err)
	}
	for _, s := range schemas {
		t, err := schemaType(s.Type)
		if err != nil {
			return nil, err
		}
		got, err := reg.CreateSchema(ctx, s.Subject, sr.Schema{Schema: s.Schema, Type: t})
		if err != nil {
			return nil, fmt.Errorf("replay: register %s v%d: %w", s.Subject, s.Version, err)
		}
		if got.Version != s.Version {
			return nil, fmt.Errorf("replay: %s registered as v%d, captured as v%d", s.Subject, got.Version, s.Version)
		}
	}
	logger := log.Nop()
	procRegistry := proc_builtin.NewRegistry(logger, proc_builtin.DefaultBuiltinProcessors, reg)
	return &Replayer{
		logger:   logger,
		registry: reg,
		service:  processor.NewService(logger, db, proc_plugin.NewPluginService(logger, procRegistry, nil)),
	}, nil
}

func schemaType(t string) (sr.SchemaType, error) {
	switch t {
	case "AVRO":
		return sr.TypeAvro, nil
	case "PROTOBUF":
		return sr.TypeProtobuf, nil
	case "JSON":
		return sr.TypeJSON, nil
	}
	return 0, fmt.Errorf("replay: unknown schema type %q", t)
}

// Registry returns the registry the processors resolve schemas from, so a
// test can assert on what a chain registered (a new subject or version).
func (r *Replayer) Registry() *schemaregistry.SchemaRegistry { return r.registry }

// Run applies the chain to each record independently and returns one Outcome
// per record, in order. A chain with no steps returns the records unchanged.
func (r *Replayer) Run(ctx context.Context, steps []Step, records []opencdc.Record) ([]Outcome, error) {
	type built struct {
		proc   *processor.RunnableProcessor
		plugin string
	}
	runnable := make([]built, 0, len(steps))
	defer func() {
		for _, b := range runnable {
			_ = b.proc.Teardown(ctx)
		}
	}()
	for i, st := range steps {
		inst, err := r.service.Create(ctx, uuid.NewString(), st.Plugin,
			processor.Parent{ID: "replay", Type: processor.ParentTypeConnector},
			processor.Config{Settings: st.Settings}, processor.ProvisionTypeAPI, st.Condition)
		if err != nil {
			return nil, fmt.Errorf("replay: step %d (%s): %w", i, st.Plugin, err)
		}
		p, err := r.service.MakeRunnableProcessor(ctx, inst)
		if err != nil {
			return nil, fmt.Errorf("replay: step %d (%s): %w", i, st.Plugin, err)
		}
		runnable = append(runnable, built{proc: p, plugin: st.Plugin})
		if err := p.Open(ctx); err != nil {
			return nil, fmt.Errorf("replay: step %d (%s): %w", i, st.Plugin, err)
		}
	}

	out := make([]Outcome, len(records))
	for i, rec := range records {
		rec, err := r.encode(ctx, rec)
		if err != nil {
			return nil, fmt.Errorf("replay: record %d: %w", i, err)
		}
		out[i] = Outcome{Record: rec, Decoded: rec}
		cur := rec
		for si, b := range runnable {
			res := b.proc.Process(ctx, []opencdc.Record{cur})
			if len(res) != 1 {
				return nil, fmt.Errorf("replay: step %d (%s) returned %d results for one record", si, b.plugin, len(res))
			}
			switch v := res[0].(type) {
			case sdk.SingleRecord:
				cur = opencdc.Record(v)
				out[i].Record = cur
			case sdk.FilterRecord:
				out[i].Filtered = true
			case sdk.ErrorRecord:
				out[i].Err = v.Error
			default:
				return nil, fmt.Errorf("replay: step %d (%s) returned %T, which the harness does not replay", si, b.plugin, v)
			}
			if out[i].Filtered || out[i].Err != nil {
				break
			}
		}
		if out[i].Filtered || out[i].Err != nil {
			continue
		}
		dec, err := r.decode(ctx, out[i].Record)
		if err != nil {
			return nil, fmt.Errorf("replay: record %d: %w", i, err)
		}
		out[i].Decoded = dec
	}
	return out, nil
}

// Records returns the captured OpenCDC records, in the order of
// smtoracle.ConduitRecords, so smtoracle.Pair.Conduit indexes the result.
func Records() ([]opencdc.Record, error) {
	b, err := smtoracle.Fixture(smtoracle.ConduitFile)
	if err != nil {
		return nil, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("replay: decode %s: %w", smtoracle.ConduitFile, err)
	}
	recs := make([]opencdc.Record, len(raw))
	for i, r := range raw {
		if err := recs[i].UnmarshalJSON(r); err != nil {
			return nil, fmt.Errorf("replay: record %d: %w", i, err)
		}
		// Decoding JSON null gives a typed nil (StructuredData(nil)), which a
		// source never emits: it leaves the interface nil. The difference
		// matters, because RunnableProcessor hands processors a Clone, and a
		// clone turns a typed nil into an empty map: a delete's After would
		// become {} before any processor ran.
		recs[i].Key = untypedNil(recs[i].Key)
		recs[i].Payload.Before = untypedNil(recs[i].Payload.Before)
		recs[i].Payload.After = untypedNil(recs[i].Payload.After)
	}
	return recs, nil
}

func untypedNil(d opencdc.Data) opencdc.Data {
	switch v := d.(type) {
	case opencdc.StructuredData:
		if v == nil {
			return nil
		}
	case opencdc.RawData:
		if v == nil {
			return nil
		}
	}
	return d
}

// decode turns the raw sides of a record that carry schema metadata into
// structured data, using the schema version the metadata names, from the
// in-memory registry.
func (r *Replayer) decode(ctx context.Context, rec opencdc.Record) (opencdc.Record, error) {
	rec = rec.Clone()
	var err error
	if rec.Key, err = r.decodeSide(ctx, rec.Key, rec.Metadata, "key"); err != nil {
		return rec, fmt.Errorf("key: %w", err)
	}
	if rec.Payload.Before, err = r.decodeSide(ctx, rec.Payload.Before, rec.Metadata, "payload"); err != nil {
		return rec, fmt.Errorf("payload.before: %w", err)
	}
	if rec.Payload.After, err = r.decodeSide(ctx, rec.Payload.After, rec.Metadata, "payload"); err != nil {
		return rec, fmt.Errorf("payload.after: %w", err)
	}
	return rec, nil
}

func (r *Replayer) decodeSide(ctx context.Context, d opencdc.Data, md opencdc.Metadata, side string) (opencdc.Data, error) {
	raw, ok := d.(opencdc.RawData)
	subject := md["opencdc."+side+".schema.subject"]
	if !ok || raw == nil || subject == "" {
		return d, nil
	}
	version, err := strconv.Atoi(md["opencdc."+side+".schema.version"])
	if err != nil {
		return nil, fmt.Errorf("schema version of %s: %w", subject, err)
	}
	ss, err := r.registry.SchemaBySubjectVersion(ctx, subject, version)
	if err != nil {
		return nil, fmt.Errorf("schema %s v%d: %w", subject, version, err)
	}
	t, err := commonsType(ss.Type)
	if err != nil {
		return nil, err
	}
	var out opencdc.StructuredData
	if err := (commonsschema.Schema{Type: t, Bytes: []byte(ss.Schema.Schema)}).Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode with %s v%d: %w", subject, version, err)
	}
	return out, nil
}

func commonsType(t sr.SchemaType) (commonsschema.Type, error) {
	switch t {
	case sr.TypeAvro:
		return commonsschema.TypeAvro, nil
	case sr.TypeProtobuf, sr.TypeJSON:
		return 0, fmt.Errorf("replay: cannot decode schema type %v yet", t)
	}
	return 0, fmt.Errorf("replay: unknown schema type %v", t)
}

// encode turns the sides of a record that carry schema metadata back into the
// raw Avro bytes the engine hands to processors: a source with a schema
// attached encodes its values before they leave the connector, and the JSON
// file the fixture is stored in cannot say which numbers were ints. Each value
// is coerced to the Go type the Avro schema expects (int, int64, *big.Rat for a
// decimal, time.Time for timestamp-micros), then marshalled with the schema
// the metadata names, from the in-memory registry.
func (r *Replayer) encode(ctx context.Context, rec opencdc.Record) (opencdc.Record, error) {
	rec = rec.Clone()
	var err error
	if rec.Key, err = r.encodeSide(ctx, rec.Key, rec.Metadata, "key"); err != nil {
		return rec, fmt.Errorf("key: %w", err)
	}
	if rec.Payload.Before, err = r.encodeSide(ctx, rec.Payload.Before, rec.Metadata, "payload"); err != nil {
		return rec, fmt.Errorf("payload.before: %w", err)
	}
	if rec.Payload.After, err = r.encodeSide(ctx, rec.Payload.After, rec.Metadata, "payload"); err != nil {
		return rec, fmt.Errorf("payload.after: %w", err)
	}
	return rec, nil
}

func (r *Replayer) encodeSide(ctx context.Context, d opencdc.Data, md opencdc.Metadata, side string) (opencdc.Data, error) {
	sd, ok := d.(opencdc.StructuredData)
	subject := md["opencdc."+side+".schema.subject"]
	if !ok || sd == nil || subject == "" {
		return d, nil
	}
	version, err := strconv.Atoi(md["opencdc."+side+".schema.version"])
	if err != nil {
		return nil, fmt.Errorf("schema version of %s: %w", subject, err)
	}
	ss, err := r.registry.SchemaBySubjectVersion(ctx, subject, version)
	if err != nil {
		return nil, fmt.Errorf("schema %s v%d: %w", subject, version, err)
	}
	t, err := commonsType(ss.Type)
	if err != nil {
		return nil, err
	}
	var schemaJSON any
	if err := json.Unmarshal([]byte(ss.Schema.Schema), &schemaJSON); err != nil {
		return nil, fmt.Errorf("schema %s v%d: %w", subject, version, err)
	}
	coerced, err := coerceAvro(schemaJSON, map[string]any(sd))
	if err != nil {
		return nil, fmt.Errorf("coerce for %s v%d: %w", subject, version, err)
	}
	b, err := (commonsschema.Schema{Type: t, Bytes: []byte(ss.Schema.Schema)}).Marshal(coerced)
	if err != nil {
		return nil, fmt.Errorf("encode with %s v%d: %w", subject, version, err)
	}
	return opencdc.RawData(b), nil
}

// coerceAvro converts a value decoded from JSON to the Go type the Avro schema
// expects. It covers what the captured schemas use: null, boolean, int, long,
// float, double, string, bytes, records, arrays, unions with null, and the
// decimal and timestamp-micros logical types. Anything else is an error, so a
// new column type fails loudly here instead of encoding something else.
func coerceAvro(schema, v any) (any, error) {
	switch sch := schema.(type) {
	case string:
		return coercePrimitive(sch, nil, v)
	case []any: // union
		if v == nil {
			return nil, nil
		}
		for _, branch := range sch {
			if b, ok := branch.(string); ok && b == "null" {
				continue
			}
			return coerceAvro(branch, v)
		}
		return nil, fmt.Errorf("union with no non-null branch")
	case map[string]any:
		switch typ, _ := sch["type"].(string); typ {
		case "record":
			m, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("record value is %T", v)
			}
			out := make(map[string]any, len(m))
			fields, _ := sch["fields"].([]any)
			for _, f := range fields {
				fm, _ := f.(map[string]any)
				name, _ := fm["name"].(string)
				fv, present := m[name]
				if !present {
					continue
				}
				c, err := coerceAvro(fm["type"], fv)
				if err != nil {
					return nil, fmt.Errorf("field %q: %w", name, err)
				}
				out[name] = c
			}
			return out, nil
		case "array":
			arr, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("array value is %T", v)
			}
			out := make([]any, len(arr))
			for i, e := range arr {
				c, err := coerceAvro(sch["items"], e)
				if err != nil {
					return nil, err
				}
				out[i] = c
			}
			return out, nil
		default:
			logical, _ := sch["logicalType"].(string)
			return coercePrimitive(typ, sch, v, logical)
		}
	}
	return nil, fmt.Errorf("unsupported Avro schema %T", schema)
}

const (
	avroInt  = "int"
	avroLong = "long"
)

func coercePrimitive(typ string, sch map[string]any, v any, logical ...string) (any, error) {
	if v == nil {
		return nil, nil
	}
	l := ""
	if len(logical) > 0 {
		l = logical[0]
	}
	switch {
	case typ == "boolean" || typ == "string":
		return v, nil
	case typ == avroInt:
		n, err := exactInt(v, math.MinInt32, math.MaxInt32)
		if err != nil {
			return nil, fmt.Errorf("int value: %w", err)
		}
		return int32(n), nil //nolint:gosec // exactInt bounded n to the int32 range
	case typ == avroLong && l == "timestamp-micros":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("timestamp value is %T", v)
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		return t, err
	case typ == avroLong:
		n, err := exactInt(v, math.MinInt64, math.MaxInt64)
		if err != nil {
			return nil, fmt.Errorf("long value: %w", err)
		}
		return n, nil
	case typ == "float" || typ == "double":
		f, ok := v.(float64)
		if !ok {
			return nil, fmt.Errorf("%s value is %T", typ, v)
		}
		return f, nil
	case typ == "bytes" && l == "decimal":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("decimal value is %T", v)
		}
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			return nil, fmt.Errorf("not a decimal: %q", s)
		}
		return r, nil
	}
	_ = sch
	return nil, fmt.Errorf("unsupported Avro type %q (logical %q)", typ, l)
}

// exactInt converts a value decoded from JSON (float64) to an integer, and
// refuses what a float64 cannot hold exactly: a fraction, or a magnitude above
// 2^53, where adjacent integers share a representation and the encoded value
// would silently be a different one.
func exactInt(v any, lo, hi int64) (int64, error) {
	f, ok := v.(float64)
	if !ok {
		return 0, fmt.Errorf("is %T, not a number", v)
	}
	const maxExact = 1 << 53
	if f != math.Trunc(f) {
		return 0, fmt.Errorf("%v is not an integer", f)
	}
	if f > maxExact || f < -maxExact {
		return 0, fmt.Errorf("%v is beyond 2^53 and cannot be represented exactly after a JSON round trip", f)
	}
	n := int64(f)
	if n < lo || n > hi {
		return 0, fmt.Errorf("%d is out of range", n)
	}
	return n, nil
}
