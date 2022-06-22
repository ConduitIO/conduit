// Copyright © 2022 Meroxa, Inc.
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

package javascript

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/dop251/goja"
	"github.com/rs/zerolog"
)

type Function struct {
	function goja.Callable
	runtime  *goja.Runtime
}

func NewFunction(src, entrypoint string, logger zerolog.Logger) (Function, error) {
	rt := goja.New()
	err := setRuntimeHelpers(logger, rt)
	if err != nil {
		return Function{}, err
	}

	prg, err := goja.Compile("", src, false)
	if err != nil {
		return Function{}, cerrors.Errorf("failed to compile transformer script: %w", err)
	}

	_, err = rt.RunProgram(prg)
	if err != nil {
		return Function{}, cerrors.Errorf("failed to run program: %w", err)
	}

	tmp := rt.Get(entrypoint)
	entrypointFunc, ok := goja.AssertFunction(tmp)
	if !ok {
		return Function{}, cerrors.Errorf("failed to get entrypoint function %q", entrypoint)
	}

	return Function{
		runtime:  rt,
		function: entrypointFunc,
	}, nil
}

// todo generics
func (f Function) Call(in record.Record) (interface{}, error) {
	jsRecord := f.toJSRecord(in)

	result, err := f.function(goja.Undefined(), jsRecord)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to transform to JS record: %w", err)
	}

	out, err := f.toInternal(result)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to transform to internal record: %w", err)
	}

	return out, nil
}

func (f Function) toJSRecord(r record.Record) goja.Value {
	// convert content to pointers to make it mutable
	switch v := r.Payload.(type) {
	case record.RawData:
		r.Payload = &v
	case record.StructuredData:
		r.Payload = &v
	}

	switch v := r.Key.(type) {
	case record.RawData:
		r.Key = &v
	case record.StructuredData:
		r.Key = &v
	}

	// we need to send in a pointer to let the user change the value and return it, if they choose to do so
	return f.runtime.ToValue(&r)
}

func (f Function) toInternal(v goja.Value) (interface{}, error) {
	r, ok := v.Export().(*record.Record)
	if !ok {
		return v.Export(), nil
	}

	// dereference content pointers
	switch v := r.Payload.(type) {
	case *record.RawData:
		r.Payload = *v
	case *record.StructuredData:
		r.Payload = *v
	}

	switch v := r.Key.(type) {
	case *record.RawData:
		r.Key = *v
	case *record.StructuredData:
		r.Key = *v
	}

	return *r, nil
}

// todo maybe move into the Function struct
func setRuntimeHelpers(logger zerolog.Logger, rt *goja.Runtime) error {
	runtimeHelpers := map[string]interface{}{
		"logger":  &logger,
		"Record":  jsRecord(rt),
		"RawData": jsContentRaw(rt),
	}

	for name, helper := range runtimeHelpers {
		if err := rt.Set(name, helper); err != nil {
			return cerrors.Errorf("failed to set helper %q: %w", name, err)
		}
	}
	return nil
}

func jsRecord(rt *goja.Runtime) func(goja.ConstructorCall) *goja.Object {
	return func(goja.ConstructorCall) *goja.Object {
		// TODO accept arguments
		// We return a record.Record struct, however because we are
		// not changing call.This instanceof will not work as expected.

		r := record.Record{
			Metadata: make(map[string]string),
		}
		// We need to return a pointer to make the returned object mutable.
		return rt.ToValue(&r).ToObject(rt)
	}
}

func jsContentRaw(rt *goja.Runtime) func(goja.ConstructorCall) *goja.Object {
	return func(goja.ConstructorCall) *goja.Object {
		// TODO accept arguments
		// We return a record.RawData struct, however because we are
		// not changing call.This instanceof will not work as expected.

		r := record.RawData{}
		// We need to return a pointer to make the returned object mutable.
		return rt.ToValue(&r).ToObject(rt)
	}
}
