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
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/recordgen"
	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"
)

// TestExclude_PropertyRemovesOnlyExcluded: field.exclude removes exactly the
// configured fields. Every record comes out, in order; each excluded field is
// gone (a field that is not there is no error); every other key, payload and
// metadata entry is unchanged.
//
// The check count follows rapid: -rapid.checks or RAPID_CHECKS (default
// 100). The nightly property job runs many more.
func TestExclude_PropertyRemovesOnlyExcluded(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(t *rapid.T) {
		in := recordgen.Batch(recordgen.StructuredData()).Draw(t, "records")

		// Field names are a small fixed set, so excluded fields are often
		// present and sometimes not. Names avoid "." and "[" so each one is a
		// single path segment.
		names := []string{"a", "b", "c", "d"}
		for i := range in {
			for _, n := range names {
				if rapid.Bool().Draw(t, "present") {
					in[i].Payload.After.(opencdc.StructuredData)[n] = recordgen.JSONValue().Draw(t, "value")
					in[i].Key.(opencdc.StructuredData)[n] = recordgen.JSONValue().Draw(t, "value")
					in[i].Metadata[n] = rapid.String().Draw(t, "metadata value")
				}
			}
		}
		type target struct{ part, name string }
		targets := rapid.SliceOfNDistinct(rapid.Custom(func(t *rapid.T) target {
			return target{
				part: rapid.SampledFrom([]string{".Payload.After", ".Key", ".Metadata"}).Draw(t, "part"),
				name: rapid.SampledFrom(names).Draw(t, "name"),
			}
		}), 1, 6, rapid.ID[target]).Draw(t, "excluded")

		want := recordgen.Clone(in)
		refs := make([]string, len(targets))
		for i, tg := range targets {
			refs[i] = tg.part + "." + tg.name
			for j := range want {
				switch tg.part {
				case ".Payload.After":
					delete(want[j].Payload.After.(opencdc.StructuredData), tg.name)
				case ".Key":
					delete(want[j].Key.(opencdc.StructuredData), tg.name)
				case ".Metadata":
					delete(want[j].Metadata, tg.name)
				}
			}
		}

		proc := NewExcludeProcessor(log.Nop())
		if err := proc.Configure(ctx, config.Config{"fields": strings.Join(refs, ",")}); err != nil {
			t.Fatalf("configure %v: %v", refs, err)
		}
		got := proc.Process(ctx, in)
		if len(got) != len(want) {
			t.Fatalf("%d records in, %d out", len(want), len(got))
		}
		for i, p := range got {
			rec, ok := p.(sdk.SingleRecord)
			if !ok {
				t.Fatalf("record %d: got %#v, want a SingleRecord", i, p)
			}
			if diff := cmp.Diff(want[i], opencdc.Record(rec), internal.CmpProcessedRecordOpts...); diff != "" {
				t.Fatalf("record %d, excluding %v (-want +got):\n%s", i, refs, diff)
			}
		}
	})
}
