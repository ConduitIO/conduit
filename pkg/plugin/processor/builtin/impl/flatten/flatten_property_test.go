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

package flatten

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/recordgen"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"pgregory.net/rapid"
)

// TestFlatten_Property checks flatten against an independent oracle over
// generated records whose field names often contain the delimiter:
//
//   - a nil side passes through, and the record is otherwise unchanged;
//   - a raw side fails with flatten.not_structured;
//   - a structured side either becomes exactly one field per leaf, named by
//     its path joined with the delimiter, with the leaf's value, or, when two
//     leaves get the same name, the record fails with flatten.key_collision
//     and is not changed. No value is ever lost or overwritten.
//
// The check count follows rapid: -rapid.checks or RAPID_CHECKS (default 100).
func TestFlatten_Property(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(t *rapid.T) {
		field := rapid.SampledFrom([]string{fieldKey, fieldBefore, fieldAfter}).Draw(t, "field")
		delimiter := rapid.SampledFrom([]string{".", "_", "a", "::"}).Draw(t, "delimiter")
		p := NewProcessor(log.Nop())
		if err := p.Configure(ctx, config.Config{"field": field, "delimiter": delimiter}); err != nil {
			t.Fatal(err)
		}
		for _, rec := range recordgen.Batch().Draw(t, "records") {
			orig := recordgen.Clone([]opencdc.Record{rec})[0]
			out := p.Process(ctx, []opencdc.Record{rec})
			if len(out) != 1 {
				t.Fatalf("got %d results", len(out))
			}
			side := sideOf(orig, field)
			switch side := side.(type) {
			case nil:
				got, ok := out[0].(sdk.SingleRecord)
				if !ok {
					t.Fatalf("expected a record, got %#v", out[0])
				}
				if diff := cmp.Diff(orig, opencdc.Record(got), cmpopts.IgnoreUnexported(opencdc.Record{})); diff != "" {
					t.Fatalf("nil side changed the record:\n%s", diff)
				}
			case opencdc.RawData:
				wantCode(t, out[0], CodeNotStructured)
			case opencdc.StructuredData:
				want, collision := oracle(side, delimiter)
				if collision {
					wantCode(t, out[0], CodeKeyCollision)
					// The failing record is not changed.
					if diff := cmp.Diff(orig, rec, cmpopts.IgnoreUnexported(opencdc.Record{})); diff != "" {
						t.Fatalf("a colliding record was changed:\n%s", diff)
					}
					continue
				}
				got, ok := out[0].(sdk.SingleRecord)
				if !ok {
					t.Fatalf("expected a record, got %#v", out[0])
				}
				if diff := cmp.Diff(want, sideOf(opencdc.Record(got), field)); diff != "" {
					t.Fatalf("flattened side differs from the oracle:\n%s", diff)
				}
			}
		}
	})
}

func sideOf(rec opencdc.Record, field string) opencdc.Data {
	switch field {
	case fieldKey:
		return rec.Key
	case fieldBefore:
		return rec.Payload.Before
	default:
		return rec.Payload.After
	}
}

// oracle lists every leaf of data under its joined path name, and reports
// whether two leaves share a name.
func oracle(data map[string]any, delimiter string) (opencdc.StructuredData, bool) {
	out := opencdc.StructuredData{}
	collision := false
	var walk func(path []string, m map[string]any)
	walk = func(path []string, m map[string]any) {
		for k, v := range m {
			p := append(append([]string{}, path...), k)
			switch nested := v.(type) {
			case map[string]any:
				if nested != nil {
					walk(p, nested)
					continue
				}
			case opencdc.StructuredData:
				if nested != nil {
					walk(p, nested)
					continue
				}
			}
			name := strings.Join(p, delimiter)
			if _, dup := out[name]; dup {
				collision = true
			}
			out[name] = v
		}
	}
	walk(nil, data)
	return out, collision
}

func wantCode(t *rapid.T, r sdk.ProcessedRecord, code conduiterr.Code) {
	er, ok := r.(sdk.ErrorRecord)
	if !ok {
		t.Fatalf("expected %s, got %#v", code.Reason(), r)
	}
	var ce *conduiterr.ConduitError
	if !cerrors.As(er.Error, &ce) || ce.Code != code {
		t.Fatalf("expected %s, got %v", code.Reason(), er.Error)
	}
}
