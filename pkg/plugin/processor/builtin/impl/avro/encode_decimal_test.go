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

package avro

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	cavro "github.com/conduitio/conduit-commons/schema/avro"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	avrointernal "github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/avro/internal"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/schemaregistrytest"
	"github.com/twmb/franz-go/pkg/sr"
)

// decimal50Schema is a pre-registered schema with a scale-0 decimal, the case
// where a negative value with a fraction used to be rounded toward negative
// infinity (-1.5 was written as -2).
const decimal50Schema = `{"type":"record","name":"dec","fields":[` +
	`{"name":"d","type":{"type":"bytes","logicalType":"decimal","precision":5,"scale":0}}]}`

func mustRat(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("bad rat %q", s)
	}
	return r
}

// TestEncodeProcessor_DecimalNotRepresentable is the conduit-level regression
// test for ConduitIO/conduit-commons#363. avro.encode used to write a decimal
// that does not fit its schema truncated, with no error: with autoRegister the
// inferred schema is decimal(31,10), so 1.123456789012 reached the topic as
// 1.1234567890. Now the record is an error record (DLQ or degraded pipeline
// per the dead-letter settings), with a code, the field and the scale the
// value needs, and never the value itself. An exact value still encodes and
// decodes to the same number.
//
// Invariant 6: schema handling never silently mangles data.
func TestEncodeProcessor_DecimalNotRepresentable(t *testing.T) {
	testCases := []struct {
		name     string
		strategy string
		value    string
		wantErr  bool
		wantCode conduiterr.Code
		wantText []string // substrings of the error message
		wantHint string   // prefix of the suggestion
	}{{
		name:     "autoRegister, more fractional digits than decimal(31,10)",
		strategy: "autoRegister",
		value:    "1.123456789012",
		wantErr:  true,
		wantCode: avrointernal.CodeDecimalNotRepresentable,
		wantText: []string{`field "d"`, "scale 12", "decimal(31,10)"},
		wantHint: avrointernal.SuggestionDecimalStrategy,
	}, {
		name:     "autoRegister, no finite decimal expansion",
		strategy: "autoRegister",
		value:    "1/3",
		wantErr:  true,
		wantCode: avrointernal.CodeDecimalNotRepresentable,
		wantText: []string{`field "d"`, "non-terminating", "decimal(31,10)"},
		wantHint: avrointernal.SuggestionDecimalStrategy,
	}, {
		name:     "autoRegister, exactly 10 fractional digits",
		strategy: "autoRegister",
		value:    "1.1234567890",
	}, {
		name:     "preRegistered, negative fraction under scale 0",
		strategy: "preRegistered",
		value:    "-1.5",
		wantErr:  true,
		wantCode: avrointernal.CodeDecimalNotRepresentable,
		wantText: []string{`field "d"`, "scale 1", "decimal(5,0)"},
		wantHint: avrointernal.SuggestionDecimalStrategy,
	}, {
		name:     "preRegistered, negative integer under scale 0",
		strategy: "preRegistered",
		value:    "-2",
	}, {
		// fromRecord with no carried schema infers decimal(31,10) like
		// autoRegister, and keeps its own code for a value finer than the
		// scale. The suggestion names the inference, not a processor.
		name:     "fromRecord fallback, more fractional digits than decimal(31,10)",
		strategy: "fromRecord",
		value:    "1.123456789012",
		wantErr:  true,
		wantCode: avrointernal.CodeSchemaMismatch,
		wantText: []string{`field "d"`, "scale 12", "decimal(31,10)"},
		wantHint: avrointernal.SuggestionDecimalInferred,
	}, {
		// fromRecord with the schema the record carries (decimal(5,0)).
		name:     "fromRecord carried, negative fraction under scale 0",
		strategy: strategyFromRecordCarried,
		value:    "-1.5",
		wantErr:  true,
		wantCode: avrointernal.CodeSchemaMismatch,
		wantText: []string{`field "d"`, "scale 1", "decimal(5,0)"},
		wantHint: avrointernal.SuggestionDecimalCarried,
	}, {
		name:     "fromRecord carried, exact value",
		strategy: strategyFromRecordCarried,
		value:    "-2",
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			p, client := newDecimalEncoder(t, tc.strategy)

			md := opencdc.Metadata{}
			if tc.strategy == strategyFromRecordCarried {
				md[opencdc.MetadataPayloadSchemaSubject] = decimalSubject(t, "src")
				md[opencdc.MetadataPayloadSchemaVersion] = "1"
			}
			value := mustRat(t, tc.value)
			got := p.Process(ctx, []opencdc.Record{{
				Operation: opencdc.OperationCreate,
				Metadata:  md,
				Payload:   opencdc.Change{After: opencdc.StructuredData{"d": value}},
			}})
			if len(got) != 1 {
				t.Fatalf("want 1 processed record, got %d", len(got))
			}
			if tc.wantErr {
				assertDecimalError(t, got[0], value, tc.wantCode, tc.wantHint, tc.wantText)
				return
			}
			d := decodeDecimal(t, client, singleRecord(t, got[0]).Payload.After.Bytes())
			if d.Cmp(value) != 0 {
				t.Fatalf("decoded %s, want %s", d.RatString(), value.RatString())
			}
		})
	}
}

// strategyFromRecordCarried is schema.strategy fromRecord on a record that
// carries decimal50Schema as version 1 of decimalSubject(t, "src").
const strategyFromRecordCarried = "fromRecord carried"

// decimalSubject returns a registry subject unique to the running (sub)test.
// CI runs these tests against one shared registry, where a fixed subject
// would make version 1 the schema an earlier subtest registered.
func decimalSubject(t *testing.T, base string) string {
	return base + "-" + strings.NewReplacer("/", "-", " ", "_", ",", "", "(", "", ")", "").Replace(t.Name()) + "-value"
}

// newDecimalEncoder returns an opened avro.encode processor with the given
// strategy and the in-memory registry it uses. For preRegistered and
// strategyFromRecordCarried it registers decimal50Schema first.
func newDecimalEncoder(t *testing.T, strategy string) (*EncodeProcessor, schemaregistry.Registry) {
	t.Helper()
	ctx := context.Background()
	client, err := schemaregistry.NewClient(log.Nop(), sr.URLs(schemaregistrytest.TestSchemaRegistryURL(t)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{"schema.strategy": strategy}
	switch strategy {
	case "autoRegister":
		cfg["schema.autoRegister.subject"] = decimalSubject(t, "dec")
	case "fromRecord":
		cfg["schema.fromRecord.subject"] = decimalSubject(t, "dec")
	case strategyFromRecordCarried:
		if _, err := client.CreateSchema(ctx, decimalSubject(t, "src"), sr.Schema{Type: sr.TypeAvro, Schema: decimal50Schema}); err != nil {
			t.Fatal(err)
		}
		cfg["schema.strategy"] = "fromRecord"
		cfg["schema.fromRecord.subject"] = decimalSubject(t, "dec")
	case "preRegistered":
		if _, err := client.CreateSchema(ctx, decimalSubject(t, "dec"), sr.Schema{Type: sr.TypeAvro, Schema: decimal50Schema}); err != nil {
			t.Fatal(err)
		}
		cfg["schema.preRegistered.subject"] = decimalSubject(t, "dec")
		cfg["schema.preRegistered.version"] = "1"
	}
	p := NewEncodeProcessor(log.Nop())
	p.SetSchemaRegistry(client)
	if err := p.Configure(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Teardown(ctx) })
	return p, client
}

// decodeDecimal decodes field "d" of a registry wire-format Avro payload.
func decodeDecimal(t *testing.T, client schemaregistry.Registry, raw []byte) *big.Rat {
	t.Helper()
	id, payload, err := (&sr.ConfluentHeader{}).DecodeID(raw)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := client.SchemaByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	serde, err := cavro.Parse([]byte(ss.Schema))
	if err != nil {
		t.Fatal(err)
	}
	var decoded opencdc.StructuredData
	if err := serde.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	d, ok := decoded["d"].(*big.Rat)
	if !ok {
		t.Fatalf("want *big.Rat, got %T", decoded["d"])
	}
	return d
}

// assertDecimalError checks that got is an error record for
// ErrDecimalNotRepresentable with the given code, a suggestion that starts
// with hint, and a message that contains wantText and no rendering of value.
func assertDecimalError(t *testing.T, got sdk.ProcessedRecord, value *big.Rat, code conduiterr.Code, hint string, wantText []string) {
	t.Helper()
	errRec, ok := got.(sdk.ErrorRecord)
	if !ok {
		t.Fatalf("want an error record, got %#v", got)
	}
	if !cerrors.Is(errRec.Error, cavro.ErrDecimalNotRepresentable) {
		t.Fatalf("want ErrDecimalNotRepresentable in the chain, got %v", errRec.Error)
	}
	ce, ok := conduiterr.Get(errRec.Error)
	if !ok {
		t.Fatalf("want a coded error, got %v", errRec.Error)
	}
	if ce.Code != code {
		t.Fatalf("want code %s, got %s: %v", code, ce.Code, errRec.Error)
	}
	if !strings.HasPrefix(ce.Suggestion, hint) {
		t.Fatalf("suggestion %q does not start with %q", ce.Suggestion, hint)
	}
	msg := errRec.Error.Error()
	for _, want := range wantText {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not contain %q", msg, want)
		}
	}
	// The value itself never appears, in any rendering: its digits, its
	// fraction, or its decimal form at the scales involved.
	for _, leak := range valueRenderings(value) {
		if strings.Contains(msg, leak) || strings.Contains(ce.Suggestion, leak) {
			t.Errorf("error %q / suggestion %q quotes the value (%q)", msg, ce.Suggestion, leak)
		}
	}
}

// valueRenderings returns the ways value could be written into a message: its
// fraction and its decimal form at several scales. Bare integers are left out,
// since the message has digit counts such as 12 and 31 of its own.
func valueRenderings(value *big.Rat) []string {
	forms := []string{value.RatString()}
	for _, n := range []int{1, 2, 10, 12, 15} {
		forms = append(forms, value.FloatString(n))
	}
	out := make([]string, 0, len(forms))
	for _, f := range forms {
		if strings.ContainsAny(f, "./") {
			out = append(out, f)
		}
	}
	return out
}
