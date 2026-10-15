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
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/recordgen"
	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"
)

type excludeTarget struct{ part, name string }

// TestExclude_PropertyRemovesOnlyExcluded: field.exclude removes exactly the
// configured fields, over a batch that mixes nil, raw and structured keys
// and payloads.
//
//   - A record whose targeted key or payload is structured or nil comes out
//     with each excluded field gone (a field that is not there is no error)
//     and every other key, payload and metadata entry unchanged.
//   - A record whose targeted key or payload is raw data fails with an
//     ErrorRecord: a field cannot be removed from bytes. The records before
//     it are processed.
//
// One known deviation is tolerated: a nil key, payload or metadata that is
// targeted comes out as an empty map instead of nil (#3147). Remove
// nilBecameEmpty when that is fixed.
//
// The check count follows rapid: -rapid.checks or RAPID_CHECKS (default
// 100). The nightly property job runs many more.
func TestExclude_PropertyRemovesOnlyExcluded(t *testing.T) {
	ctx := context.Background()
	rapid.Check(t, func(t *rapid.T) {
		in := recordgen.Batch().Draw(t, "records")

		// Field names are a small fixed set, so excluded fields are often
		// present and sometimes not. Names avoid "." and "[" so each one is a
		// single path segment.
		names := []string{"a", "b", "c", "d"}
		for i := range in {
			for _, n := range names {
				if !rapid.Bool().Draw(t, "present") {
					continue
				}
				if sd, ok := in[i].Payload.After.(opencdc.StructuredData); ok {
					sd[n] = recordgen.JSONValue().Draw(t, "value")
				}
				if sd, ok := in[i].Key.(opencdc.StructuredData); ok {
					sd[n] = recordgen.JSONValue().Draw(t, "value")
				}
				if in[i].Metadata != nil {
					in[i].Metadata[n] = rapid.String().Draw(t, "metadata value")
				}
			}
		}
		targets := rapid.SliceOfNDistinct(rapid.Custom(func(t *rapid.T) excludeTarget {
			return excludeTarget{
				part: rapid.SampledFrom([]string{".Payload.After", ".Key", ".Metadata"}).Draw(t, "part"),
				name: rapid.SampledFrom(names).Draw(t, "name"),
			}
		}), 1, 6, rapid.ID[excludeTarget]).Draw(t, "excluded")

		want := recordgen.Clone(in)
		failAt := -1
		refs := make([]string, len(targets))
		for i, tg := range targets {
			refs[i] = tg.part + "." + tg.name
		}
		for j := range want {
			for _, tg := range targets {
				var data opencdc.Data
				switch tg.part {
				case ".Payload.After":
					data = want[j].Payload.After
				case ".Key":
					data = want[j].Key
				case ".Metadata":
					delete(want[j].Metadata, tg.name)
					continue
				}
				switch d := data.(type) {
				case opencdc.StructuredData:
					delete(d, tg.name)
				case opencdc.RawData:
					if failAt < 0 {
						failAt = j
					}
				}
			}
		}

		proc := NewExcludeProcessor(log.Nop())
		if err := proc.Configure(ctx, config.Config{"fields": strings.Join(refs, ",")}); err != nil {
			t.Fatalf("configure %v: %v", refs, err)
		}
		if err := recordgen.CheckOutput(proc.Process(ctx, in), want, failAt, nilBecameEmpty); err != nil {
			t.Fatal(err)
		}
	})
}

// nilBecameEmpty treats a nil record key, Payload.After or metadata as
// equal to an empty one, and nothing else: nested values still compare
// exactly. See #3147.
var nilBecameEmpty = cmp.FilterPath(
	func(p cmp.Path) bool {
		switch p.String() {
		case "Key", "Payload.After", "Metadata":
			return true
		}
		return false
	},
	cmp.FilterValues(
		func(a, b any) bool { return isNilOrEmpty(a) && isNilOrEmpty(b) },
		cmp.Comparer(func(_, _ any) bool { return true }),
	),
)

func isNilOrEmpty(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case opencdc.StructuredData:
		return len(v) == 0
	case opencdc.Metadata:
		return len(v) == 0
	}
	return false
}
