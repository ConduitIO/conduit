// Copyright © 2023 Meroxa, Inc.
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
	"reflect"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/hamba/avro/v2"
	"github.com/modern-go/reflect2"
)

type MapUnionResolver struct {
	mapUnionPaths []path
	resolver      *avro.TypeResolver
}

func NewMapUnionResolver(schema avro.Schema) MapUnionResolver {
	var paths []path
	// traverse the schema and extract paths to all maps with a union as the
	// value type
	traverseSchema(schema, func(p path) {
		s, ok := p[len(p)-1].schema.(*avro.MapSchema)
		if !ok || s.Values().Type() != avro.Union {
			return // we are only searching for map union schemas
		}
		// this is what we're looking for, store it
		pCopy := make(path, len(p))
		copy(pCopy, p)
		paths = append(paths, pCopy)
	})
	return MapUnionResolver{
		mapUnionPaths: paths,
		resolver:      avro.NewTypeResolver(),
	}
}

func (r MapUnionResolver) AfterUnmarshal(val any) error {
	if len(r.mapUnionPaths) == 0 {
		return nil // shortcut
	}

	type substitution struct {
		m   map[string]any
		key string
		val any
	}
	var substitutions []substitution

	for _, p := range r.mapUnionPaths {
		// first collect all maps that have a union type in the schema
		var maps []map[string]any
		err := traverseValue(val, p, true, func(v any) {
			if mapUnion, ok := v.(map[string]any); ok {
				maps = append(maps, mapUnion)
			}
		})
		if err != nil {
			return err
		}

		// Loop through collected maps and collect all substitutions. These maps
		// contain values encoded as maps with a single key:value pair, where
		// key is the type name (e.g. {"int":1}). We want to replace all these
		// maps with the actual value (e.g. 1).
		// We don't replace them in the loop, because we want to make sure all
		// maps actually contain only 1 value.
		for i, mapUnion := range maps {
			for k, v := range mapUnion {
				if v == nil {
					// do no change nil values
					continue
				}
				vmap, ok := v.(map[string]any)
				if !ok {
					return cerrors.Errorf("expected map[string]any, got %T", v)
				}
				if len(vmap) != 1 {
					return cerrors.Errorf("expected single value encoded as a map, got %d elements", len(vmap))
				}

				// this is a map with a single value, store the substitution
				for _, actualVal := range vmap {
					substitutions = append(substitutions, substitution{
						m:   maps[i],
						key: k,
						val: actualVal,
					})
					break
				}
			}
		}
	}
	// We now have a list of substitutions, simply apply them.
	for _, sub := range substitutions {
		sub.m[sub.key] = sub.val
	}
	return nil
}

func (r MapUnionResolver) BeforeMarshal(val any) error {
	if len(r.mapUnionPaths) == 0 {
		return nil // shortcut
	}

	type substitution struct {
		m           map[string]any
		key         string
		valTypeName string
	}
	var substitutions []substitution

	for _, p := range r.mapUnionPaths {
		mapSchema, ok := p[len(p)-1].schema.(*avro.MapSchema)
		if !ok {
			return cerrors.Errorf("expected *avro.MapSchema, got %T", p[len(p)-1].schema)
		}
		unionSchema, ok := mapSchema.Values().(*avro.UnionSchema)
		if !ok {
			return cerrors.Errorf("expected *avro.UnionSchema, got %T", mapSchema.Values())
		}

		// first collect all maps that have a union type in the schema
		var maps []map[string]any
		err := traverseValue(val, p, false, func(v any) {
			if mapUnion, ok := v.(map[string]any); ok {
				maps = append(maps, mapUnion)
			}
		})
		if err != nil {
			return err
		}

		// Loop through collected maps and collect all substitutions. We want
		// to replace all non-nil values in these maps with maps that contain a
		// single value, the key corresponds to the resolved name.
		// We don't replace them in the loop, because we want to make sure all
		// type names can be resolved first.
		for i, mapUnion := range maps {
			for k, v := range mapUnion {
				if v == nil {
					// do no change nil values
					continue
				}
				name, err := r.resolveNameForType(v, unionSchema)
				if err != nil {
					return err
				}
				substitutions = append(substitutions, substitution{
					m:           maps[i],
					key:         k,
					valTypeName: name,
				})
			}
		}
	}

	// We now have a list of substitutions, simply apply them.
	for _, sub := range substitutions {
		v := sub.m[sub.key]
		sub.m[sub.key] = map[string]any{sub.valTypeName: v}
	}
	return nil
}

func (r MapUnionResolver) resolveNameForType(v any, us *avro.UnionSchema) (string, error) {
	var names []string

	t := reflect2.TypeOf(v)
	switch t.Kind() {
	case reflect.Map:
		names = []string{"map"}
	case reflect.Slice:
		if !t.Type1().Elem().AssignableTo(byteType) { // []byte is handled differently
			names = []string{"array"}
			break
		}
		fallthrough
	default:
		var err error
		names, err = r.resolver.Name(t)
		if err != nil {
			return "", err
		}
	}

	for _, n := range names {
		_, pos := us.Types().Get(n)
		if pos > -1 {
			return n, nil
		}
	}
	return "", cerrors.Errorf("can't resolve %v in union type %v", names, us.String())
}
