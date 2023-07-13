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
	"fmt"
	"reflect"
	"sort"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/hamba/avro/v2"
)

type (
	// path represents a path from the root to a certain type in an avro schema.
	path []leg
	// leg is a single leg of a path.
	leg struct {
		schema avro.Schema
		field  *avro.Field
	}
)

// traverseSchema is a utility for traversing an avro schema and executing fn on
// every schema in the tree.
func traverseSchema(s avro.Schema, fn func(path)) {
	var traverse func(avro.Schema, path)
	traverse = func(s avro.Schema, p path) {
		p = append(p, leg{s, nil})
		fn(p)

		// traverse deeper into types that have nested types
		switch s := s.(type) {
		case *avro.MapSchema:
			traverse(s.Values(), p)
		case *avro.ArraySchema:
			traverse(s.Items(), p)
		case *avro.RefSchema:
			traverse(s.Schema(), p)
		case *avro.RecordSchema:
			fields := s.Fields()
			p = p[:len(p)-1]
			for _, field := range fields {
				p = append(p, leg{s, field})
				traverse(field.Type(), p)
				p = p[:len(p)-1]
			}
		case *avro.UnionSchema:
			for _, st := range s.Types() {
				traverse(st, p)
			}
		}
	}
	traverse(s, nil)
}

// sortFn can be passed to traverse to deterministically sort fields in every
// record and types in every union.
func sortFn(p path) {
	switch s := p[len(p)-1].schema.(type) {
	case *avro.RecordSchema:
		fields := s.Fields()
		sort.Slice(fields, func(i, j int) bool {
			return fields[i].Name() < fields[j].Name()
		})
	case *avro.UnionSchema:
		schemas := s.Types()
		sort.Slice(schemas, func(i, j int) bool {
			return schemas[i].String() < schemas[j].String()
		})
	}
}

// traverseValue is a utility to traverse val down to the path and call fn with
// all values found at the end of the path. If hasEncodedUnions is set to true,
// any map and array with a union type is expected to contain a map[string]any
// with a single key representing the name of the type it contains
// (e.g. {"int": 1}).
// If the value structure does not match the path p, traverseValue returns an
// error.
//
//nolint:gocyclo // need to switch on avro type and have a case for each type
func traverseValue(val any, p path, hasEncodedUnions bool, fn func(v any)) error {
	var traverse func(any, int) error
	traverse = func(val any, index int) error {
		if index == len(p)-1 {
			// reached the end of the path, call fn
			fn(val)
			return nil
		}
		if val == nil {
			return nil // can't traverse further, not an error though
		}
		switch l := p[index]; l.schema.Type() {
		case avro.Record:
			switch val := val.(type) {
			case map[string]any:
				return traverse(val[l.field.Name()], index+1)
			case record.StructuredData:
				return traverse(val[l.field.Name()], index+1)
			case *map[string]any:
				return traverse(*val, index) // traverse value
			case *record.StructuredData:
				return traverse(*val, index) // traverse value
			}
			return newUnexpectedTypeError(avro.Record, map[string]any{}, val)
		case avro.Array:
			valArr, ok := val.([]any)
			if !ok {
				return newUnexpectedTypeError(avro.Array, []any{}, val)
			}
			for _, item := range valArr {
				if err := traverse(item, index+1); err != nil {
					return err
				}
			}
		case avro.Map:
			valMap, ok := val.(map[string]any)
			if !ok {
				return newUnexpectedTypeError(avro.Map, map[string]any{}, val)
			}
			for _, v := range valMap {
				if err := traverse(v, index+1); err != nil {
					return err
				}
			}
		case avro.Ref:
			// ignore ref and go deeper
			return traverse(val, index+1)
		case avro.Union:
			if hasEncodedUnions && index > 0 &&
				(p[index-1].schema.Type() == avro.Map || p[index-1].schema.Type() == avro.Array) {
				// it's a union value encoded as a map, traverse it
				valMap, ok := val.(map[string]any)
				if !ok {
					return newUnexpectedTypeError(avro.Union, map[string]any{}, val)
				}
				if len(valMap) != 1 {
					return cerrors.Errorf("expected single value encoded as a map, got %d elements", len(valMap))
				}
				for _, v := range valMap {
					return traverse(v, index+1) // there's only one value, return
				}
			}

			// values are encoded normally, skip union
			err := traverse(val, index+1)
			var uterr *unexpectedTypeError
			if cerrors.As(err, &uterr) {
				// We allow unexpected type errors, we could be traversing a
				// different branch in the union type that does not have the
				// same structure.
				return nil
			}
			return err
		default:
			return cerrors.Errorf("unexpected avro type %s, can not traverse deeper", l.schema.Type())
		}
		return nil
	}
	return traverse(val, 0)
}

type unexpectedTypeError struct {
	avroType       avro.Type
	expectedGoType string
	actualGoType   string
}

func newUnexpectedTypeError(avroType avro.Type, expected any, actual any) *unexpectedTypeError {
	return &unexpectedTypeError{
		avroType:       avroType,
		expectedGoType: reflect.TypeOf(expected).String(),
		actualGoType:   reflect.TypeOf(actual).String(),
	}
}

func (e *unexpectedTypeError) Error() string {
	return fmt.Sprintf("expected Go type %s for avro type %s, got %s", e.expectedGoType, e.avroType, e.actualGoType)
}
