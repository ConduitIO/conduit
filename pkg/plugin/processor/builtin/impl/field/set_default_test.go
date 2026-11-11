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
	"bytes"
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"testing"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/tplerr"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/recordgen"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/matryer/is"
	"pgregory.net/rapid"
)

// setV010 is a frozen copy of field.set v0.1.0 (Configure after config
// parsing, and Process). It is the reference TestSetField_DefaultIsUnchanged
// compares the current processor with. Do not edit it.
type setV010 struct {
	referenceResolver sdk.ReferenceResolver
	tmpl              *template.Template
}

// configureV010 is v0.1.0's Configure, parameters included.
func (p *setV010) configureV010(ctx context.Context, c config.Config) error {
	cfg := struct {
		Field string `json:"field"`
		Value string `json:"value"`
	}{}
	params := config.Parameters{
		"field": {Type: config.ParameterTypeString, Validations: []config.Validation{
			config.ValidationRequired{}, config.ValidationExclusion{List: []string{".Position"}},
		}},
		"value": {Type: config.ParameterTypeString, Validations: []config.Validation{config.ValidationRequired{}}},
	}
	err := sdk.ParseConfig(ctx, c, &cfg, params)
	if err != nil {
		return fmt.Errorf("failed to parse configuration: %w", err)
	}
	return p.configure(cfg.Field, cfg.Value)
}

func (p *setV010) configure(field, value string) error {
	tmpl, err := template.New("").Funcs(sprig.FuncMap()).Parse(value)
	if err != nil {
		return fmt.Errorf("failed to parse the %q param template: %w", "value", err)
	}
	p.tmpl = tmpl
	resolver, err := sdk.NewReferenceResolver(field)
	if err != nil {
		return fmt.Errorf("failed to parse the %q param: %w", "field", err)
	}
	p.referenceResolver = resolver
	return nil
}

func (p *setV010) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		rec := record
		var b bytes.Buffer
		// evaluate the new value
		err := p.tmpl.Execute(&b, rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: tplerr.Exec(err)})
		}
		ref, err := p.referenceResolver.Resolve(&rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		err = ref.Set(b.String())
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, sdk.SingleRecord(rec))
	}
	return out
}

// Existing configurations: every target kind, and values that are literals
// (including one that looks like a field reference), templates, and
// templates that fail at run time.
var (
	defaultFields = []string{
		".Metadata.table", ".Operation", ".Key", ".Key.id", ".Payload.After",
		".Payload.After.id", ".Payload.Before.a.b", `.Payload.After["x y"]`,
		".Payload.Before", ".Metadata", ".payload.after.id", ".key", "$.Key.id",
		`.Payload["After"]`, `.metadata["opencdc.collection"]`,
	}
	defaultValues = []string{
		"postgres",
		".Payload.After.id", // a literal string, never a copy of that field
		".Key",
		"{{ .Payload.After.a }}",
		"{{ .Payload.After }}",
		"{{ .Key }}",
		`{{ index .Metadata "opencdc.collection" }}`,
		`{{ mustToDate "2006-01-02" (index .Metadata "d") }}`, // fails, the error must be wrapped by tplerr.Exec
		"{{ .Payload.After.a.b.c }}",                          // fails on raw data
		"{{ fail \"boom\" }}",
		"create",
	}
)

// fixedRecords covers every side shape once: nil, raw and structured keys
// and payloads, a delete (nil after-image), a tombstone (nil key and
// payload), nested data, and schema metadata.
func fixedRecords() []opencdc.Record {
	md := opencdc.Metadata{"d": "not-a-date", "opencdc.collection": "users"}
	md.SetKeySchemaSubject("k")
	md.SetKeySchemaVersion(1)
	md.SetPayloadSchemaSubject("p")
	md.SetPayloadSchemaVersion(2)
	return []opencdc.Record{
		{},
		{Metadata: md, Key: opencdc.RawData("k1"), Payload: opencdc.Change{After: opencdc.RawData(`{"id":1}`)}},
		{Metadata: md, Key: opencdc.StructuredData{"id": 1}, Payload: opencdc.Change{
			Before: opencdc.StructuredData{"a": map[string]any{"b": 1}},
			After:  opencdc.StructuredData{"id": int64(7), "a": map[string]any{"b": map[string]any{"c": "x"}}, "x y": true},
		}},
		{Operation: opencdc.OperationDelete, Key: opencdc.StructuredData{"id": 1}, Payload: opencdc.Change{Before: opencdc.StructuredData{"id": 1}}},
		{Operation: opencdc.OperationDelete, Metadata: opencdc.Metadata{}},
		{Payload: opencdc.Change{After: opencdc.StructuredData{"a": "scalar"}}},
	}
}

// TestSetField_DefaultIsUnchanged pins the default behaviour (only "field"
// and "value" set) to v0.1.0: for every existing configuration above and
// every side shape, the processor returns the same records and the same
// errors as the frozen copy, including template errors wrapped by
// tplerr.Exec. The auto-detect copy mode of the WIP (a value starting with
// "." copies that field) fails it on value ".Payload.After.id".
func TestSetField_DefaultIsUnchanged(t *testing.T) {
	ctx := context.Background()
	for _, field := range defaultFields {
		for _, value := range defaultValues {
			ref, p := defaultPair(t, field, value)
			for i, rec := range fixedRecords() {
				want := ref.Process(ctx, []opencdc.Record{recordgen.Clone([]opencdc.Record{rec})[0]})
				got := p.Process(ctx, []opencdc.Record{recordgen.Clone([]opencdc.Record{rec})[0]})
				if diff := cmpProcessed(want, got); diff != "" {
					t.Errorf("field %q, value %q, record %d: output differs from v0.1.0 (-v0.1.0 +now):\n%s", field, value, i, diff)
				}
			}
		}
	}
}

// TestSetField_DefaultIsUnchangedProperty is the same comparison over
// generated batches, which also covers a failure in the middle of a batch.
func TestSetField_DefaultIsUnchangedProperty(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(rt *rapid.T) {
		field := rapid.SampledFrom(defaultFields).Draw(rt, "field")
		value := rapid.SampledFrom(defaultValues).Draw(rt, "value")
		ref, p := defaultPair(rt, field, value)
		in := recordgen.Batch().Draw(rt, "records")
		for i := range in {
			if rapid.Bool().Draw(rt, "schema metadata") {
				if in[i].Metadata == nil {
					in[i].Metadata = opencdc.Metadata{}
				}
				in[i].Metadata.SetPayloadSchemaSubject("p")
				in[i].Metadata.SetPayloadSchemaVersion(1)
				in[i].Metadata.SetKeySchemaSubject("k")
				in[i].Metadata.SetKeySchemaVersion(1)
				in[i].Metadata["d"] = "not-a-date"
			}
		}
		want := ref.Process(ctx, recordgen.Clone(in))
		got := p.Process(ctx, recordgen.Clone(in))
		if diff := cmpProcessed(want, got); diff != "" {
			rt.Fatalf("output differs from v0.1.0 (-v0.1.0 +now):\n%s", diff)
		}
	})
}

func defaultPair(t interface{ Fatalf(string, ...any) }, field, value string) (*setV010, *SetProcessor) {
	ref := &setV010{}
	if err := ref.configure(field, value); err != nil {
		t.Fatalf("reference configure: %v", err)
	}
	p := NewSetProcessor(log.Nop())
	if err := p.Configure(context.Background(), config.Config{"field": field, "value": value}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return ref, p
}

// TestSetField_ConfigureIsUnchanged: for v0.1.0 configurations, valid and
// invalid, Configure accepts and rejects the same ones as v0.1.0, with the
// same error text, also when several parameters are invalid at once.
func TestSetField_ConfigureIsUnchanged(t *testing.T) {
	ctx := context.Background()
	for name, cfg := range map[string]config.Config{
		"valid":                         {"field": ".Payload.After.x", "value": "{{ .Key }}"},
		"value missing":                 {"field": ".Key"},
		"value empty":                   {"field": ".Key", "value": ""},
		"field missing":                 {"value": "x"},
		"both missing":                  {},
		"position":                      {"field": ".Position", "value": "x"},
		"position and no value":         {"field": ".Position"},
		"invalid template":              {"field": ".Key", "value": "{{ invalid }}"},
		"invalid field":                 {"field": ".Payload.Middle", "value": "x"},
		"invalid field and template":    {"field": ".Payload.Middle", "value": "{{ invalid }}"},
		"unclosed template":             {"field": ".Key", "value": "{{ .Key"},
		"field with a space":            {"field": ".Key. a", "value": "x"},
		"lower-case and $ spellings ok": {"field": "$.Payload.After.x", "value": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			wantErr := (&setV010{}).configureV010(ctx, maps.Clone(cfg))
			gotErr := NewSetProcessor(log.Nop()).Configure(ctx, maps.Clone(cfg))
			if (wantErr == nil) != (gotErr == nil) || (wantErr != nil && errLines(wantErr) != errLines(gotErr)) {
				t.Fatalf("Configure differs from v0.1.0:\nv0.1.0: %v\nnow:    %v", wantErr, gotErr)
			}
		})
	}
}

// errLines returns the lines of err's text in sorted order. The SDK joins
// several validation errors in map order, so v0.1.0 itself reports them in
// varying order; the set of errors is what must not change.
func errLines(err error) string {
	text := err.Error()
	prefix, rest, _ := strings.Cut(text, "error validating")
	parts := strings.Split("error validating"+rest, "\n")
	sort.Strings(parts)
	return prefix + "|" + strings.Join(parts, "\n")
}

// TestSetField_LiteralThatLooksLikeAReference is the explicit case of the
// breaking change the auto-detect design would have made.
func TestSetField_LiteralThatLooksLikeAReference(t *testing.T) {
	is := is.New(t)
	p := NewSetProcessor(log.Nop())
	is.NoErr(p.Configure(context.Background(), config.Config{"field": ".Payload.After.out", "value": ".Payload.After.id"}))
	out := p.Process(context.Background(), []opencdc.Record{{
		Payload: opencdc.Change{After: opencdc.StructuredData{"id": 7}},
	}})
	is.Equal(out, []sdk.ProcessedRecord{sdk.SingleRecord{
		Payload: opencdc.Change{After: opencdc.StructuredData{"id": 7, "out": ".Payload.After.id"}},
	}})
}

// cmpProcessed compares two outputs. Records are compared by value; errors
// by their text and by the type of every error in their chain, so a change
// in wrapping fails as well as a change in wording.
func cmpProcessed(want, got []sdk.ProcessedRecord) string {
	return cmp.Diff(processedView(want), processedView(got), cmpopts.IgnoreUnexported(opencdc.Record{}))
}

func processedView(out []sdk.ProcessedRecord) []any {
	view := make([]any, len(out))
	for i, r := range out {
		switch r := r.(type) {
		case sdk.ErrorRecord:
			chain := []string{}
			for err := r.Error; err != nil; {
				chain = append(chain, fmt.Sprintf("%T: %s", err, err.Error()))
				u, ok := err.(interface{ Unwrap() error })
				if !ok {
					break
				}
				err = u.Unwrap()
			}
			view[i] = chain
		case sdk.SingleRecord:
			view[i] = opencdc.Record(r)
		default:
			view[i] = fmt.Sprintf("%T", r)
		}
	}
	return view
}
