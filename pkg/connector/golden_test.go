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

// Pre-flip gates for the arch-v2 default flip (v0.20). The persisted
// connector.Instance blob is plain JSON with no version/schema tag anywhere,
// and Store.decode never sets DisallowUnknownFields - so an older binary
// reading a newer blob silently drops fields it doesn't recognize instead of
// erroring. Worse, Store.PrepareSet copies into an explicit whitelist struct
// (icopy) rather than marshalling the instance directly, so a field added to
// Instance is silently un-persisted unless someone remembers to also add it
// to that whitelist.
//
// These tests make both failure modes loud instead of silent:
//
//   - TestGolden_*_RoundTrip checks in the actual JSON currently persisted
//     for a source and a destination connector instance and asserts HEAD's
//     Store can parse it into a semantically identical Instance AND
//     re-serialize it back to an equivalent blob. If a future change drops
//     or renames a field, the re-serialized blob loses a key the checked-in
//     golden still has, and the semantic JSON comparison fails.
//   - TestStore_PrepareSet_WhitelistCoversAllFields populates every exported
//     field of Instance via reflection (so it does not need to be
//     hand-updated when a field is added), round-trips it through the real
//     Store.Set/db path, and diffs what actually got persisted against a
//     direct marshal of the same Instance. Any field PrepareSet's icopy
//     whitelist forgets to copy shows up as a concrete, named diff.
package connector

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

// assertJSONSemanticallyEqual parses both blobs into generic
// map[string]any values and compares them, so differences in key order or
// whitespace don't matter but a dropped, renamed, or zeroed-out field does.
func assertJSONSemanticallyEqual(t *testing.T, is *is.I, golden, got []byte) {
	t.Helper()

	var goldenMap, gotMap map[string]any
	is.NoErr(json.Unmarshal(golden, &goldenMap))
	is.NoErr(json.Unmarshal(got, &gotMap))

	if !reflect.DeepEqual(goldenMap, gotMap) {
		t.Fatalf(
			"re-serialized instance is not semantically equal to the golden blob "+
				"(a field was likely dropped or renamed):\ngolden: %s\ngot:    %s",
			golden, got,
		)
	}
}

func TestGolden_SourceInstance_RoundTrip(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	golden, err := os.ReadFile("testdata/golden_source_instance.json")
	is.NoErr(err)

	db := &inmemory.DB{}
	s := NewStore(db, log.Nop())
	is.NoErr(db.Set(ctx, s.addKeyPrefix("golden-source"), golden))

	got, err := s.Get(ctx, "golden-source")
	is.NoErr(err)

	ts := time.Date(2026, 6, 15, 9, 30, 0, 0, time.UTC)
	want := &Instance{
		ID:   "golden-source",
		Type: TypeSource,
		Config: Config{
			Name:     "golden-source-connector",
			Settings: map[string]string{"path": "/data/in"},
		},
		PipelineID:   "golden-pipeline",
		Plugin:       "builtin:file",
		ProcessorIDs: []string{"proc-1", "proc-2"},
		State: SourceState{
			Position: opencdc.Position("golden-position-42"),
		},
		ProvisionedBy: ProvisionTypeConfig,
		CreatedAt:     ts,
		UpdatedAt:     ts.Add(time.Minute),
		LastActiveConfig: Config{
			Name:     "golden-source-connector",
			Settings: map[string]string{"path": "/data/in"},
		},
	}
	is.Equal(want, got)

	// The actual drop/rename detector: re-encode what HEAD decoded and
	// compare against the golden blob itself, not just against `want` (which
	// a contributor could forget to update in lockstep with a struct change).
	reEncoded, err := s.encode(got)
	is.NoErr(err)
	assertJSONSemanticallyEqual(t, is, golden, reEncoded)
}

func TestGolden_DestinationInstance_RoundTrip(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	golden, err := os.ReadFile("testdata/golden_destination_instance.json")
	is.NoErr(err)

	db := &inmemory.DB{}
	s := NewStore(db, log.Nop())
	is.NoErr(db.Set(ctx, s.addKeyPrefix("golden-destination"), golden))

	got, err := s.Get(ctx, "golden-destination")
	is.NoErr(err)

	ts := time.Date(2026, 6, 15, 9, 30, 0, 0, time.UTC)
	want := &Instance{
		ID:   "golden-destination",
		Type: TypeDestination,
		Config: Config{
			Name:     "golden-destination-connector",
			Settings: map[string]string{"path": "/data/out"},
		},
		PipelineID:   "golden-pipeline",
		Plugin:       "builtin:file",
		ProcessorIDs: []string{"proc-3"},
		State: DestinationState{
			Positions: map[string]opencdc.Position{
				"golden-source": opencdc.Position("golden-position-42"),
			},
		},
		ProvisionedBy: ProvisionTypeConfig,
		CreatedAt:     ts,
		UpdatedAt:     ts.Add(time.Minute),
		LastActiveConfig: Config{
			Name:     "golden-destination-connector",
			Settings: map[string]string{"path": "/data/out"},
		},
	}
	is.Equal(want, got)

	reEncoded, err := s.encode(got)
	is.NoErr(err)
	assertJSONSemanticallyEqual(t, is, golden, reEncoded)
}

// fillNonZero generically populates v with a distinct, non-zero value for
// common kinds (string, int-family, uint-family, bool, slice, map, struct -
// recursing into nested structs and specializing time.Time). It exists so
// TestStore_PrepareSet_WhitelistCoversAllFields does not need to be
// hand-updated every time a field is added to Instance: a new field of any
// of these common kinds gets a real value automatically, so the whitelist
// coverage check actually exercises it.
//
// Known limitation: fields of interface, pointer, chan, or func kind are
// left as their zero value (nothing sensible to synthesize generically).
// Instance currently has one such field (State any), which callers must set
// explicitly before/after calling fillNonZero.
func fillNonZero(v reflect.Value, seed *int) {
	if !v.CanSet() {
		return
	}

	switch v.Kind() {
	case reflect.String:
		*seed++
		v.SetString(fieldSeedString(*seed))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		*seed++
		v.SetInt(int64(*seed))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		*seed++
		v.SetUint(uint64(*seed))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Slice:
		elem := reflect.New(v.Type().Elem()).Elem()
		fillNonZero(elem, seed)
		s := reflect.MakeSlice(v.Type(), 1, 1)
		s.Index(0).Set(elem)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		key := reflect.New(v.Type().Key()).Elem()
		fillNonZero(key, seed)
		val := reflect.New(v.Type().Elem()).Elem()
		fillNonZero(val, seed)
		m.SetMapIndex(key, val)
		v.Set(m)
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			*seed++
			v.Set(reflect.ValueOf(time.Date(2026, 1, 1, 0, 0, *seed, 0, time.UTC)))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath != "" {
				continue // unexported, not serialized, skip
			}
			fillNonZero(v.Field(i), seed)
		}
	case reflect.Invalid, reflect.Uintptr, reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128, reflect.Array, reflect.Chan,
		reflect.Func, reflect.Interface, reflect.Pointer, reflect.UnsafePointer:
		// Not synthesized: Instance has no fields of these kinds today
		// besides State (any), which callers set explicitly. See the doc
		// comment above.
	}
}

func fieldSeedString(seed int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	return "val-" + string(alphabet[seed%len(alphabet)]) + string(rune('0'+seed%10))
}

// TestStore_PrepareSet_WhitelistCoversAllFields is the trap-detector from
// the task: Store.PrepareSet builds its own whitelist copy (icopy) of
// Instance instead of marshalling the instance it was given. This test
// populates every exported top-level field of Instance via reflection
// (State is set explicitly per subtest, see fillNonZero's doc comment),
// persists it through the real Store.Set/db path, and diffs what actually
// got persisted against a direct marshal of the same Instance. If
// PrepareSet's icopy ever forgets a field, that field's real value from the
// direct marshal will not match the zero value written by the whitelist
// copy, and the diff names the exact field.
func TestStore_PrepareSet_WhitelistCoversAllFields(t *testing.T) {
	for _, connType := range []Type{TypeSource, TypeDestination} {
		t.Run(connType.String(), func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			want := &Instance{Type: connType}
			seed := 0
			v := reflect.ValueOf(want).Elem()
			tp := v.Type()
			for i := 0; i < tp.NumField(); i++ {
				f := tp.Field(i)
				if f.PkgPath != "" {
					continue // unexported runtime field (logger, persister, ...), not serialized
				}
				switch f.Name {
				case "Type", "State":
					continue // set explicitly below, must stay consistent with connType
				}
				fillNonZero(v.Field(i), &seed)
			}
			switch connType {
			case TypeSource:
				want.State = SourceState{Position: opencdc.Position("whitelist-position")}
			case TypeDestination:
				want.State = DestinationState{
					Positions: map[string]opencdc.Position{"partition-0": opencdc.Position("whitelist-position")},
				}
			}

			db := &inmemory.DB{}
			s := NewStore(db, log.Nop())
			is.NoErr(s.Set(ctx, want.ID, want))

			// wantRaw: what SHOULD be persisted - a direct marshal of the
			// fully-populated instance HEAD was handed.
			wantRaw, err := json.Marshal(want)
			is.NoErr(err)

			// storedRaw: what PrepareSet's icopy whitelist ACTUALLY wrote.
			storedRaw, err := db.Get(ctx, s.addKeyPrefix(want.ID))
			is.NoErr(err)

			assertJSONSemanticallyEqual(t, is, wantRaw, storedRaw)
		})
	}
}
