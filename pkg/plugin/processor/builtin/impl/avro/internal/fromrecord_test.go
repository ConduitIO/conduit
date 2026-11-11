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
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	condprom "github.com/conduitio/conduit/pkg/foundation/metrics/prometheus"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/schemaregistrytest"
	"github.com/goccy/go-json"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/sr"
)

// sourceSchema is the schema the Postgres source attaches to a table with an
// int4, a numeric(14,2) and a date column (smoke-20261105, wide table), plus
// a nullable int to cover unions.
const sourceSchema = `{"type":"record","name":"bench_source_0","namespace":"public","fields":[` +
	`{"name":"c00_int","type":"int"},` +
	`{"name":"c02_numeric","type":["null",{"type":"bytes","logicalType":"decimal","precision":14,"scale":2}]},` +
	`{"name":"c06_date","type":{"type":"int","logicalType":"date"}},` +
	`{"name":"c10_opt","type":["null","int"]}]}`

// sourceRow is a row as the processor SDK decode middleware hands it to
// avro.encode: Avro int is a Go int, a decimal a *big.Rat, a date a
// time.Time at midnight UTC.
func sourceRow() opencdc.StructuredData {
	return opencdc.StructuredData{
		"c00_int":     32,
		"c02_numeric": big.NewRat(3232, 100),
		"c06_date":    time.Date(2023, 11, 14, 0, 0, 0, 0, time.UTC),
		"c10_opt":     nil,
	}
}

type fromRecordEnv struct {
	client  *schemaregistry.Client
	carried SchemaRef
	enc     *Encoder
}

// newFromRecordEnv registers carriedSchema under a source subject in a fresh
// in-memory registry and returns an Encoder with the fromRecord strategy.
func newFromRecordEnv(t *testing.T, carriedSchema string) fromRecordEnv {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()
	client, err := schemaregistry.NewClient(log.Nop(), sr.URLs(schemaregistrytest.TestSchemaRegistryURL(t)))
	is.NoErr(err)
	ss, err := client.CreateSchema(ctx, "source.bench_source_0.payload", sr.Schema{Type: sr.TypeAvro, Schema: carriedSchema})
	is.NoErr(err)
	return fromRecordEnv{
		client:  client,
		carried: SchemaRef{Subject: ss.Subject, Version: ss.Version},
		enc:     NewEncoder(client, log.Nop(), NewFromRecordSchemaStrategy("cdc.public.bench_source_0-value")),
	}
}

// writerSchema returns the schema behind the wire header of raw and the
// decoded body.
func writerSchema(t *testing.T, client schemaregistry.Registry, raw opencdc.RawData) (map[string]any, opencdc.StructuredData) {
	t.Helper()
	is := is.New(t)
	var h sr.ConfluentHeader
	id, body, err := h.DecodeID(raw)
	is.NoErr(err)
	s, err := client.SchemaByID(context.Background(), id)
	is.NoErr(err)
	var parsed map[string]any
	is.NoErr(json.Unmarshal([]byte(s.Schema), &parsed))
	var decoded opencdc.StructuredData
	is.NoErr((schema.Schema{Type: schema.TypeAvro, Bytes: []byte(s.Schema)}).Unmarshal(body, &decoded))
	return parsed, decoded
}

// fieldTypes maps each field name of a parsed record schema to its type,
// re-encoded as compact JSON so it compares as text.
func fieldTypes(t *testing.T, parsed map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range parsed["fields"].([]any) {
		fm := f.(map[string]any)
		b, err := json.Marshal(fm["type"])
		if err != nil {
			t.Fatal(err)
		}
		out[fm["name"].(string)] = string(b)
	}
	return out
}

func wantCode(t *testing.T, err error, code conduiterr.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error with code %s, got nil", code)
	}
	ce, ok := conduiterr.Get(err)
	if !ok {
		t.Fatalf("want error with code %s, got uncoded error: %v", code, err)
	}
	if ce.Code != code {
		t.Fatalf("want code %s, got %s: %v", code, ce.Code, err)
	}
}

// TestFromRecord_KeepsSourceTypes reproduces the smoke-20261105 finding: a
// record carrying int, decimal(14,2) and date, with an added __op field. The
// schema behind the wire header must have the source types, plus a nullable
// __op; autoRegister infers long, decimal(31,10) and timestamp-micros.
func TestFromRecord_KeepsSourceTypes(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	env := newFromRecordEnv(t, sourceSchema)

	in := sourceRow()
	in["__op"] = "c"
	raw, err := env.enc.Encode(ctx, in, env.carried)
	is.NoErr(err)

	parsed, decoded := writerSchema(t, env.client, raw)
	want := map[string]string{
		"c00_int":     `"int"`,
		"c02_numeric": `["null",{"logicalType":"decimal","precision":14,"scale":2,"type":"bytes"}]`,
		"c06_date":    `{"logicalType":"date","type":"int"}`,
		"c10_opt":     `["null","int"]`,
		"__op":        `["null","string"]`,
	}
	is.Equal("", cmp.Diff(want, fieldTypes(t, parsed)))
	is.Equal(parsed["name"], "bench_source_0")
	is.Equal(parsed["namespace"], "public")
	is.True(sameValue(in, decoded)) // values are unchanged

	// The added field defaults to null, so the extended schema can read data
	// written with the carried one.
	for _, f := range parsed["fields"].([]any) {
		fm := f.(map[string]any)
		if fm["name"] == "__op" {
			d, ok := fm["default"]
			is.True(ok)
			is.Equal(d, nil)
		}
	}
}

// TestAutoRegister_InfersFromValues pins the behavior fromRecord is the
// opt-in alternative to: autoRegister is unchanged.
func TestAutoRegister_InfersFromValues(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	env := newFromRecordEnv(t, sourceSchema)
	enc := NewEncoder(env.client, log.Nop(), ExtractAndUploadSchemaStrategy{Subject: "auto-value"})

	in := sourceRow()
	in["c10_opt"] = 1
	in["__op"] = "c"
	raw, err := enc.Encode(ctx, in, env.carried)
	is.NoErr(err)

	parsed, _ := writerSchema(t, env.client, raw)
	got := fieldTypes(t, parsed)
	is.Equal(got["c00_int"], `"long"`)
	is.Equal(got["c06_date"], `{"logicalType":"timestamp-micros","type":"long"}`)
	is.Equal(got["c02_numeric"], `["null",{"logicalType":"decimal","precision":31,"scale":10,"type":"bytes"}]`)
}

func TestFromRecord_NothingAdded_UsesCarriedSchemaText(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	env := newFromRecordEnv(t, sourceSchema)

	raw, err := env.enc.Encode(ctx, sourceRow(), env.carried)
	is.NoErr(err)
	var h sr.ConfluentHeader
	id, _, err := h.DecodeID(raw)
	is.NoErr(err)
	s, err := env.client.SchemaByID(ctx, id)
	is.NoErr(err)
	is.Equal(s.Schema, sourceSchema)
}

func TestFromRecord_SameAddedFields_SameSchemaID(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	env := newFromRecordEnv(t, sourceSchema)

	ids := map[int]struct{}{}
	for _, op := range []any{"c", "u", nil} {
		in := sourceRow()
		in["__op"] = op
		in["__table"] = "bench_source_0"
		raw, err := env.enc.Encode(ctx, in, env.carried)
		is.NoErr(err)
		var h sr.ConfluentHeader
		id, _, err := h.DecodeID(raw)
		is.NoErr(err)
		ids[id] = struct{}{}
	}
	is.Equal(len(ids), 1) // a nil __op infers ["null","string"], the same as "c"
}

// TestFromRecord_Mismatch covers every way a value can stop fitting the
// carried schema. Each must fail with CodeSchemaMismatch, naming the field,
// and never encode a changed value.
func TestFromRecord_Mismatch(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(opencdc.StructuredData)
		field  string
	}{{
		name:   "type changed by a processor",
		mutate: func(sd opencdc.StructuredData) { sd["c00_int"] = "32" },
		field:  "c00_int",
	}, {
		name:   "int out of range",
		mutate: func(sd opencdc.StructuredData) { sd["c00_int"] = int64(1) << 40 },
		field:  "c00_int",
	}, {
		name:   "decimal finer than the scale",
		mutate: func(sd opencdc.StructuredData) { sd["c02_numeric"] = big.NewRat(1234, 1000) },
		field:  "c02_numeric",
	}, {
		name:   "time of day in a date",
		mutate: func(sd opencdc.StructuredData) { sd["c06_date"] = time.Date(2023, 11, 14, 1, 0, 0, 0, time.UTC) },
		field:  "c06_date",
	}, {
		name:   "null in a non-null field",
		mutate: func(sd opencdc.StructuredData) { sd["c00_int"] = nil },
		field:  "c00_int",
	}, {
		// A removed field is dropped from the schema; the fields still
		// present keep their carried types, so a mistyped one still fails.
		name: "field removed, another present with the wrong type",
		mutate: func(sd opencdc.StructuredData) {
			delete(sd, "c10_opt")
			sd["c00_int"] = "32"
		},
		field: "c00_int",
	}, {
		// A key present with nil is null, not a removed field: it is not
		// dropped, and null does not fit the non-null int.
		name: "field removed, another present with null in a non-null field",
		mutate: func(sd opencdc.StructuredData) {
			delete(sd, "c02_numeric")
			sd["c00_int"] = nil
		},
		field: "c00_int",
	}, {
		name: "field removed, another present and finer than its scale",
		mutate: func(sd opencdc.StructuredData) {
			delete(sd, "c00_int")
			sd["c02_numeric"] = big.NewRat(1234, 1000)
		},
		field: "c02_numeric",
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			env := newFromRecordEnv(t, sourceSchema)
			in := sourceRow()
			tc.mutate(in)
			raw, err := env.enc.Encode(ctx, in, env.carried)
			wantCode(t, err, CodeSchemaMismatch)
			if len(raw) != 0 {
				t.Fatalf("want no bytes, got %d", len(raw))
			}
			ce, _ := conduiterr.Get(err)
			if want := `field "` + tc.field + `"`; !strings.Contains(ce.Message, want) {
				t.Fatalf("message %q does not name %s", ce.Message, want)
			}
		})
	}
}

func TestFromRecord_NullableAcceptsValueAndNull(t *testing.T) {
	ctx := context.Background()
	for _, v := range []any{nil, 7} {
		is := is.New(t)
		env := newFromRecordEnv(t, sourceSchema)
		in := sourceRow()
		in["c10_opt"] = v
		in["c02_numeric"] = nil
		raw, err := env.enc.Encode(ctx, in, env.carried)
		is.NoErr(err)
		parsed, decoded := writerSchema(t, env.client, raw)
		is.True(sameValue(in, decoded))
		is.Equal(fieldTypes(t, parsed)["c10_opt"], `["null","int"]`) // the carried union, not one inferred from the value
	}
}

func TestFromRecord_MultiBranchUnion(t *testing.T) {
	const unionSchema = `{"type":"record","name":"u","fields":[{"name":"v","type":["null","int","string"]}]}`
	ctx := context.Background()
	for _, v := range []any{nil, 5, "five"} {
		is := is.New(t)
		env := newFromRecordEnv(t, unionSchema)
		in := opencdc.StructuredData{"v": v}
		raw, err := env.enc.Encode(ctx, in, env.carried)
		is.NoErr(err)
		parsed, decoded := writerSchema(t, env.client, raw)
		is.True(sameValue(in, decoded))
		is.Equal(fieldTypes(t, parsed)["v"], `["null","int","string"]`)
	}
}

// TestFromRecord_NestedAddedKey: fields are added at the top level only. A
// key a processor added inside a nested record would be dropped by the codec,
// so it must be an error.
func TestFromRecord_NestedAddedKey(t *testing.T) {
	const nestedSchema = `{"type":"record","name":"outer","fields":[` +
		`{"name":"inner","type":{"type":"record","name":"inner_t","fields":[{"name":"a","type":"int"}]}}]}`
	ctx := context.Background()
	env := newFromRecordEnv(t, nestedSchema)
	in := opencdc.StructuredData{"inner": map[string]any{"a": 1, "added": "x"}}
	_, err := env.enc.Encode(ctx, in, env.carried)
	wantCode(t, err, CodeSchemaMismatch)
}

func TestFromRecord_CarriedSchemaNotARecord(t *testing.T) {
	ctx := context.Background()
	env := newFromRecordEnv(t, `"string"`)
	_, err := env.enc.Encode(ctx, opencdc.StructuredData{"a": "x"}, env.carried)
	wantCode(t, err, CodeSchemaUnsupported)
}

func TestFromRecord_AddedFieldCannotBeAdded(t *testing.T) {
	ctx := context.Background()
	env := newFromRecordEnv(t, sourceSchema)
	in := sourceRow()
	in["not-an-avro-name"] = "x"
	_, err := env.enc.Encode(ctx, in, env.carried)
	wantCode(t, err, CodeSchemaUnsupported)
}

// fakeRegistry injects registry failures in front of a real client.
type fakeRegistry struct {
	schemaregistry.Registry
	fetchErr  error
	createErr error
	fetchType sr.SchemaType
}

func (f fakeRegistry) SchemaBySubjectVersion(ctx context.Context, subject string, version int) (sr.SubjectSchema, error) {
	if f.fetchErr != nil {
		return sr.SubjectSchema{}, f.fetchErr
	}
	ss, err := f.Registry.SchemaBySubjectVersion(ctx, subject, version)
	ss.Type = f.fetchType
	return ss, err //nolint:wrapcheck // test fake
}

func (f fakeRegistry) CreateSchema(ctx context.Context, subject string, s sr.Schema) (sr.SubjectSchema, error) {
	if f.createErr != nil {
		return sr.SubjectSchema{}, f.createErr
	}
	return f.Registry.CreateSchema(ctx, subject, s) //nolint:wrapcheck // test fake
}

func TestFromRecord_RegistryFailures(t *testing.T) {
	conflict := cerrors.Errorf("register: %w", &sr.ResponseError{StatusCode: http.StatusConflict, ErrorCode: 409, Message: "incompatible"})
	testCases := []struct {
		name string
		reg  func(schemaregistry.Registry) fakeRegistry
		want conduiterr.Code
	}{{
		name: "carried schema cannot be fetched",
		reg: func(r schemaregistry.Registry) fakeRegistry {
			return fakeRegistry{Registry: r, fetchErr: cerrors.New("connection refused")}
		},
		want: CodeRegistryUnavailable,
	}, {
		name: "carried schema is not Avro",
		reg: func(r schemaregistry.Registry) fakeRegistry {
			return fakeRegistry{Registry: r, fetchType: sr.TypeProtobuf}
		},
		want: CodeSchemaUnsupported,
	}, {
		name: "registration refused by compatibility",
		reg: func(r schemaregistry.Registry) fakeRegistry {
			return fakeRegistry{Registry: r, createErr: conflict}
		},
		want: CodeSchemaIncompatible,
	}, {
		name: "registration fails",
		reg: func(r schemaregistry.Registry) fakeRegistry {
			return fakeRegistry{Registry: r, createErr: cerrors.New("connection refused")}
		},
		want: CodeRegistryUnavailable,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			env := newFromRecordEnv(t, sourceSchema)
			enc := NewEncoder(tc.reg(env.client), log.Nop(), NewFromRecordSchemaStrategy("out-value"))
			// The fake would not fall back to inference either way: a
			// failure is an error, never an inferred schema.
			raw, err := enc.Encode(ctx, sourceRow(), env.carried)
			wantCode(t, err, tc.want)
			if len(raw) != 0 {
				t.Fatalf("want no bytes, got %d", len(raw))
			}
		})
	}
}

// TestFromRecord_MixedRecords: records of different tables in one processor
// each encode with their own schema.
func TestFromRecord_MixedRecords(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	env := newFromRecordEnv(t, sourceSchema)
	other, err := env.client.CreateSchema(ctx, "source.other.payload", sr.Schema{
		Type:   sr.TypeAvro,
		Schema: `{"type":"record","name":"other","fields":[{"name":"x","type":"int"}]}`,
	})
	is.NoErr(err)
	otherRef := SchemaRef{Subject: other.Subject, Version: other.Version}

	for i := 0; i < 2; i++ {
		raw, err := env.enc.Encode(ctx, sourceRow(), env.carried)
		is.NoErr(err)
		parsed, _ := writerSchema(t, env.client, raw)
		is.Equal(parsed["name"], "bench_source_0")

		raw, err = env.enc.Encode(ctx, opencdc.StructuredData{"x": 1}, otherRef)
		is.NoErr(err)
		parsed, _ = writerSchema(t, env.client, raw)
		is.Equal(parsed["name"], "other")
		is.Equal(fieldTypes(t, parsed)["x"], `"int"`)
	}
}

// TestFromRecord_NoSchema_FallsBackAndCounts: a record without a schema is
// encoded by inference, and counted.
func TestFromRecord_NoSchema_FallsBackAndCounts(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	promReg := registerFreshBackend(is)
	env := newFromRecordEnv(t, sourceSchema)
	enc := NewEncoder(env.client, log.Nop(), NewFromRecordSchemaStrategy("fallback-value"))

	raw, err := enc.Encode(ctx, opencdc.StructuredData{"n": 1}, SchemaRef{})
	is.NoErr(err)
	parsed, _ := writerSchema(t, env.client, raw)
	is.Equal(fieldTypes(t, parsed)["n"], `"long"`) // inferred

	_, err = enc.Encode(ctx, opencdc.StructuredData{"n": 2}, SchemaRef{})
	is.NoErr(err)
	is.Equal(fallbackCount(t, promReg, "fallback-value"), 2.0)
}

// TestFromRecord_NoSchema_StillLossless: the fallback inference maps a
// *big.Rat to decimal(31,10). With fromRecord a value with a finer scale is an
// error, not a truncated decimal (the codec truncates on the released
// conduit-commons).
func TestFromRecord_NoSchema_StillLossless(t *testing.T) {
	ctx := context.Background()
	env := newFromRecordEnv(t, sourceSchema)
	r, ok := new(big.Rat).SetString("1.123456789012")
	if !ok {
		t.Fatal("bad rat")
	}
	_, err := env.enc.Encode(ctx, opencdc.StructuredData{"d": r}, SchemaRef{})
	wantCode(t, err, CodeSchemaMismatch)
}

func registerFreshBackend(is *is.I) *promclient.Registry {
	reg := condprom.NewRegistry(nil)
	metrics.Register(reg)
	promReg := promclient.NewRegistry()
	is.NoErr(promReg.Register(reg))
	return promReg
}

func fallbackCount(t *testing.T, promReg *promclient.Registry, subject string) float64 {
	t.Helper()
	families, err := promReg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "conduit_processor_avro_encode_schema_fallback_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "subject" && l.GetValue() == subject {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestFromRecord_RemovedFieldIsDropped: a top-level field the record no
// longer has (field.exclude before avro.encode) is left out of the encoding
// schema, instead of being written as its default or null. The other fields
// keep their carried types.
func TestFromRecord_RemovedFieldIsDropped(t *testing.T) {
	testCases := []struct {
		name    string
		removed string
	}{
		{name: "non-null field", removed: "c00_int"},
		{name: "nullable field", removed: "c02_numeric"},
		{name: "nullable field holding null", removed: "c10_opt"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			env := newFromRecordEnv(t, sourceSchema)

			ids := map[int]struct{}{}
			for i := 0; i < 2; i++ {
				in := sourceRow()
				delete(in, tc.removed)
				raw, err := env.enc.Encode(ctx, in, env.carried)
				is.NoErr(err)

				parsed, decoded := writerSchema(t, env.client, raw)
				want := map[string]string{
					"c00_int":     `"int"`,
					"c02_numeric": `["null",{"logicalType":"decimal","precision":14,"scale":2,"type":"bytes"}]`,
					"c06_date":    `{"logicalType":"date","type":"int"}`,
					"c10_opt":     `["null","int"]`,
				}
				delete(want, tc.removed)
				is.Equal("", cmp.Diff(want, fieldTypes(t, parsed)))
				is.Equal(parsed["name"], "bench_source_0")
				is.True(sameValue(in, decoded)) // nothing added back, nothing changed
				_, ok := decoded[tc.removed]
				is.True(!ok)

				var h sr.ConfluentHeader
				id, _, err := h.DecodeID(raw)
				is.NoErr(err)
				ids[id] = struct{}{}
			}
			is.Equal(len(ids), 1) // the same removal gives the same schema ID
		})
	}
}

// TestFromRecord_RemovedAndAddedFields: a field removed and another added in
// the same record.
func TestFromRecord_RemovedAndAddedFields(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	env := newFromRecordEnv(t, sourceSchema)

	in := sourceRow()
	delete(in, "c10_opt")
	in["__op"] = "d"
	raw, err := env.enc.Encode(ctx, in, env.carried)
	is.NoErr(err)

	parsed, decoded := writerSchema(t, env.client, raw)
	want := map[string]string{
		"c00_int":     `"int"`,
		"c02_numeric": `["null",{"logicalType":"decimal","precision":14,"scale":2,"type":"bytes"}]`,
		"c06_date":    `{"logicalType":"date","type":"int"}`,
		"__op":        `["null","string"]`,
	}
	is.Equal("", cmp.Diff(want, fieldTypes(t, parsed)))
	is.True(sameValue(in, decoded))
}

// TestFromRecord_RemovedNestedKey: only top-level fields are dropped. A key
// removed inside a nested record would be written as its default or null by
// the codec, so it must be an error.
func TestFromRecord_RemovedNestedKey(t *testing.T) {
	const nestedSchema = `{"type":"record","name":"outer","fields":[` +
		`{"name":"inner","type":{"type":"record","name":"inner_t","fields":[` +
		`{"name":"a","type":"int"},{"name":"b","type":["null","int"],"default":null}]}}]}`
	ctx := context.Background()
	env := newFromRecordEnv(t, nestedSchema)
	in := opencdc.StructuredData{"inner": map[string]any{"a": 1}}
	raw, err := env.enc.Encode(ctx, in, env.carried)
	wantCode(t, err, CodeSchemaMismatch)
	if len(raw) != 0 {
		t.Fatalf("want no bytes, got %d", len(raw))
	}
}

// TestFromRecord_RemovedFieldDefinesNamedType: when the removed field holds
// the definition of a named type that a kept field refers to by name, the
// schema cannot be fitted. It fails closed instead of guessing a definition.
func TestFromRecord_RemovedFieldDefinesNamedType(t *testing.T) {
	const namedSchema = `{"type":"record","name":"outer","fields":[` +
		`{"name":"a","type":{"type":"enum","name":"color","symbols":["RED","BLUE"]}},` +
		`{"name":"b","type":"color"}]}`
	ctx := context.Background()
	env := newFromRecordEnv(t, namedSchema)
	_, err := env.enc.Encode(ctx, opencdc.StructuredData{"b": "RED"}, env.carried)
	wantCode(t, err, CodeSchemaUnsupported)

	// Removing the field that only refers to the type is fine.
	raw, err := env.enc.Encode(ctx, opencdc.StructuredData{"a": "BLUE"}, env.carried)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := writerSchema(t, env.client, raw)
	if got := fieldTypes(t, parsed); len(got) != 1 || got["a"] == "" {
		t.Fatalf("want only field a, got %v", got)
	}
}

// TestFromRecord_LocalTimestampWallClock: the codec writes a local-timestamp
// value with the wall clock the instant has in time.Local and reads it back
// as the same instant, so the instant comparison passes while a reader sees a
// different wall clock. A time in another zone must fail; one whose zone
// matches time.Local, or UTC when Local is UTC, must not. Not parallel: it
// sets time.Local.
func TestFromRecord_LocalTimestampWallClock(t *testing.T) {
	prev := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = prev })

	const lts = `{"type":"record","name":"lt","namespace":"public","fields":[` +
		`{"name":"a","type":{"type":"long","logicalType":"local-timestamp-micros"}},` +
		`{"name":"b","type":["null",{"type":"long","logicalType":"local-timestamp-millis"}]},` +
		`{"name":"n","type":{"type":"record","name":"inner","fields":[` +
		`{"name":"t","type":{"type":"long","logicalType":"local-timestamp-micros"}}]}},` +
		`{"name":"u","type":{"type":"long","logicalType":"timestamp-micros"}}]}`
	utc := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	plus5 := time.Date(2024, 1, 1, 10, 0, 0, 0, time.FixedZone("x", 5*3600))
	row := func(a, b, nested, u time.Time) opencdc.StructuredData {
		return opencdc.StructuredData{"a": a, "b": b, "n": map[string]any{"t": nested}, "u": u}
	}

	testCases := []struct {
		name  string
		in    opencdc.StructuredData
		field string // "" means it must encode
	}{
		{"all UTC", row(utc, utc, utc, utc), ""},
		{"zone offset on a timestamp (UTC-instant) field is fine", row(utc, utc, utc, plus5), ""},
		{"non-UTC in a local-timestamp field", row(plus5, utc, utc, utc), "a"},
		{"non-UTC in a nullable local-timestamp field", row(utc, plus5, utc, utc), "b"},
		{"non-UTC in a nested local-timestamp field", row(utc, utc, plus5, utc), "n"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			env := newFromRecordEnv(t, lts)
			_, err := env.enc.Encode(context.Background(), tc.in, env.carried)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			wantCode(t, err, CodeSchemaMismatch)
			ce, _ := conduiterr.Get(err)
			if want := `field "` + tc.field + `"`; !strings.Contains(ce.Message, want) {
				t.Fatalf("message %q does not name %s", ce.Message, want)
			}
		})
	}
}
