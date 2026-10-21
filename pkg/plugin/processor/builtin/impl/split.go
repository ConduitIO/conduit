// Copyright © 2025 Meroxa, Inc.
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

//go:generate paramgen -output=split_paramgen.go splitConfig

package impl

import (
	"context"
	"reflect"
	"strconv"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

type SplitProcessor struct {
	sdk.UnimplementedProcessor

	referenceResolver sdk.ReferenceResolver
	config            splitConfig
}

func NewSplitProcessor(log.CtxLogger) *SplitProcessor { return &SplitProcessor{} }

type splitConfig struct {
	// Field is the target field that should be split. Note that the target
	// field has to contain an array so it can be split, otherwise the processor
	// returns an error. This also means you can only split on fields in
	// structured data under `.Key` and `.Payload`.
	//
	// For more information about the format, see [Referencing fields](https://conduitdata.io/docs/using/processors/referencing-fields).
	Field string `json:"field" validate:"required,regex=^\\.(Payload|Key).*"`
}

func (p *SplitProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "split",
		Summary: "Split records.",
		Description: `Split the records into multiple records based on the field
provided in the configuration. If the field is an array, each element of the
array will be converted into a separate record. The new record will contain the
same data as the original record, but the field specified in the configuration
will be replaced with the element of the array. The index of the element in the
array will be stored in the metadata of the new record under the key
` + "`split.index`" + `.

This processor is only applicable to ` + "`.Key`" + `, ` + "`.Payload.Before`" + `
and ` + "`.Payload.After`" + ` prefixes, and only applicable if said fields
contain structured data. If the record contains raw JSON data, then use the
processor [` + "`json.decode`" + `](/docs/using/processors/builtin/json.decode)
to parse it into structured data first.

**Important:** This processor currently only works using the pipeline architecture
v2, which can be enabled using the flag ` + "`--preview.pipeline-arch-v2`" + `.
Using it without the flag will result in an error.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: splitConfig{}.Parameters(),
	}, nil
}

func (p *SplitProcessor) Configure(ctx context.Context, c config.Config) error {
	err := sdk.ParseConfig(ctx, c, &p.config, splitConfig{}.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}

	resolver, err := sdk.NewReferenceResolver(p.config.Field)
	if err != nil {
		return cerrors.Errorf("failed to parse the %q param: %w", "field", err)
	}
	p.referenceResolver = resolver
	return nil
}

func (p *SplitProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		// Read the slice from a private copy: resolving creates missing parent
		// fields, and that must not touch the caller's record.
		work := isolatedClone(rec)
		fillNilData(&work)
		var val reflect.Value
		err := p.withReference(&work, func(ref sdk.Reference) error {
			val = reflect.ValueOf(ref.Get())
			return nil
		})
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		if val.Kind() != reflect.Slice {
			return append(out, sdk.ErrorRecord{
				Error: cerrors.Errorf("field %q is not a slice", p.config.Field),
			})
		}

		mr := make(sdk.MultiRecord, val.Len())
		for i := 0; i < val.Len(); i++ {
			newRec := isolatedClone(rec)
			// Never discard an error here: a nil reference used to panic the
			// whole process (#3152). Invariant 3: a record we can't split is
			// returned as an ErrorRecord so the pipeline nacks it (DLQ or
			// halt); it is never dropped.
			err := p.withReference(&newRec, func(ref sdk.Reference) error {
				if err := ref.Set(deepCopy(val.Index(i).Interface())); err != nil {
					return cerrors.Errorf("failed to set the item: %w", err)
				}
				return nil
			})
			if err != nil {
				return append(out, sdk.ErrorRecord{Error: err})
			}
			if newRec.Metadata == nil {
				newRec.Metadata = make(map[string]string)
			}
			newRec.Metadata["split.index"] = strconv.Itoa(i)

			mr[i] = newRec
		}
		out = append(out, mr)
	}
	return out
}

// isolatedClone returns rec.Clone() with every structured value (key, payload
// before, payload after) deep-copied, keeping each value's Go type.
//
// The types are exactly what Record.Clone produces, whichever conduit-commons
// version is in use: the output of split matches the output of Clone. What
// Clone does not guarantee, depending on the version, is isolation: it may
// share nested slices and nested opencdc.StructuredData with rec. Output
// records of split must share nothing with each other or with the input.
func isolatedClone(rec opencdc.Record) opencdc.Record {
	c := rec.Clone()
	c.Key = isolateData(c.Key)
	c.Payload.Before = isolateData(c.Payload.Before)
	c.Payload.After = isolateData(c.Payload.After)
	return c
}

func isolateData(d opencdc.Data) opencdc.Data {
	if sd, ok := d.(opencdc.StructuredData); ok {
		//nolint:forcetypeassert // deepCopy preserves the type.
		return deepCopy(sd).(opencdc.StructuredData)
	}
	return d
}

// fillNilData replaces typed-nil structured data with an empty map. The
// reference resolver assigns into the map when it creates missing parent
// fields, which panics on a nil map. Only used on a throwaway copy.
func fillNilData(rec *opencdc.Record) {
	fill := func(d opencdc.Data) opencdc.Data {
		if sd, ok := d.(opencdc.StructuredData); ok && sd == nil {
			return opencdc.StructuredData{}
		}
		return d
	}
	rec.Key = fill(rec.Key)
	rec.Payload.Before = fill(rec.Payload.Before)
	rec.Payload.After = fill(rec.Payload.After)
}

// withReference resolves the configured field in rec, calls fn with the
// reference, and puts rec's nested map types back as they were.
//
// The reference resolver only walks map[string]any, but records carry nested
// maps of both map[string]any and opencdc.StructuredData (Record.Clone retypes
// them in some conduit-commons versions). For the duration of the call every
// nested StructuredData reachable through maps is replaced by a
// map[string]any view of the same storage, then swapped back, so the caller
// sees the original types and a single code path works for both.
//
// rec must be private to the caller: the views are written into its maps.
// A panic in the resolver (it assigns into nil maps in some cases) is turned
// into an error, because nothing recovers on the processor path.
func (p *SplitProcessor) withReference(rec *opencdc.Record, fn func(sdk.Reference) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = cerrors.Errorf("failed to resolve field %q: %v", p.config.Field, r)
		}
	}()

	var views []mapView
	for _, d := range []opencdc.Data{rec.Key, rec.Payload.Before, rec.Payload.After} {
		if sd, ok := d.(opencdc.StructuredData); ok && sd != nil {
			views = plainViews(sd, views)
		}
	}
	defer func() {
		for _, v := range views {
			v.restore()
		}
	}()

	ref, err := p.referenceResolver.Resolve(rec)
	if err != nil {
		return err //nolint:wrapcheck // the resolver's error already names the path.
	}
	return fn(ref)
}

// mapView records a nested map entry that was replaced by a plain
// map[string]any view.
type mapView struct {
	parent map[string]any
	key    string
	orig   any
	view   map[string]any
}

func (v mapView) restore() {
	// If the entry was replaced meanwhile (e.g. by Set), leave it alone.
	cur, ok := v.parent[v.key].(map[string]any)
	if ok && reflect.ValueOf(cur).Pointer() == reflect.ValueOf(v.view).Pointer() {
		v.parent[v.key] = v.orig
	}
}

func plainViews(m map[string]any, views []mapView) []mapView {
	for k, v := range m {
		switch c := v.(type) {
		case opencdc.StructuredData:
			view := map[string]any(c)
			if c == nil {
				// A nil map can't be walked into either; use an empty one for
				// the duration of the call.
				view = map[string]any{}
			}
			m[k] = view
			views = append(views, mapView{parent: m, key: k, orig: c, view: view})
			views = plainViews(view, views)
		case map[string]any:
			if c != nil {
				views = plainViews(c, views)
			}
		}
	}
	return views
}

// deepCopy copies maps, slices and arrays at every depth and keeps every
// value's Go type (map[string]any stays map[string]any, opencdc.StructuredData
// stays StructuredData, nil stays nil). Pointers and struct fields are
// shared.
func deepCopy(v any) any {
	switch v := v.(type) {
	case nil:
		return nil
	case string, bool, int, int64, float64:
		return v
	case map[string]any:
		if v == nil {
			return v
		}
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = deepCopy(e)
		}
		return out
	case opencdc.StructuredData:
		if v == nil {
			return v
		}
		out := make(opencdc.StructuredData, len(v))
		for k, e := range v {
			out[k] = deepCopy(e)
		}
		return out
	case []any:
		if v == nil {
			return v
		}
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = deepCopy(e)
		}
		return out
	}
	return copyValue(reflect.ValueOf(v)).Interface()
}

func copyValue(rv reflect.Value) reflect.Value {
	switch rv.Kind() { //nolint:exhaustive // other kinds are copied by value.
	case reflect.Map:
		if rv.IsNil() {
			return rv
		}
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		for it := rv.MapRange(); it.Next(); {
			out.SetMapIndex(it.Key(), copyValue(it.Value()))
		}
		return out
	case reflect.Slice:
		if rv.IsNil() {
			return rv
		}
		out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		if isScalarKind(rv.Type().Elem().Kind()) {
			reflect.Copy(out, rv)
			return out
		}
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(copyValue(rv.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(rv.Type()).Elem()
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(copyValue(rv.Index(i)))
		}
		return out
	case reflect.Interface:
		if rv.IsNil() {
			return rv
		}
		out := reflect.New(rv.Type()).Elem()
		out.Set(copyValue(rv.Elem()))
		return out
	default:
		return rv
	}
}

func isScalarKind(k reflect.Kind) bool {
	switch k { //nolint:exhaustive // only scalar kinds matter.
	case reflect.Bool, reflect.String, reflect.Float32, reflect.Float64,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	default:
		return false
	}
}
