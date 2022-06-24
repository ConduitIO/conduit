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

package jsprocessor

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/dop251/goja"
	"github.com/rs/zerolog"
)

const (
	entrypoint = "process"
)

// jsProcessor is able to run transformations defined in JavaScript.
type jsProcessor struct {
	runtime  *goja.Runtime
	function goja.Callable
}

func NewJSProcessor(src string, logger zerolog.Logger) (processor.Processor, error) {
	p := &jsProcessor{}
	err := p.initJSRuntime(logger)
	if err != nil {
		return nil, cerrors.Errorf("failed initializing JS runtime: %w", err)
	}

	err = p.initFunction(src)
	if err != nil {
		return nil, cerrors.Errorf("failed initializing JS function: %w", err)
	}

	return p, nil
}

func (p *jsProcessor) initJSRuntime(logger zerolog.Logger) error {
	rt := goja.New()
	runtimeHelpers := map[string]interface{}{
		"logger":  &logger,
		"Record":  p.jsRecord,
		"RawData": p.jsContentRaw,
	}

	for name, helper := range runtimeHelpers {
		if err := rt.Set(name, helper); err != nil {
			return cerrors.Errorf("failed to set helper %q: %w", name, err)
		}
	}

	p.runtime = rt
	return nil
}

func (p *jsProcessor) initFunction(src string) error {
	prg, err := goja.Compile("", src, false)
	if err != nil {
		return cerrors.Errorf("failed to compile transformer script: %w", err)
	}

	_, err = p.runtime.RunProgram(prg)
	if err != nil {
		return cerrors.Errorf("failed to run program: %w", err)
	}

	tmp := p.runtime.Get(entrypoint)
	entrypointFunc, ok := goja.AssertFunction(tmp)
	if !ok {
		return cerrors.Errorf("failed to get entrypoint function %q", entrypoint)
	}

	p.function = entrypointFunc
	return nil
}

func (p *jsProcessor) jsRecord(goja.ConstructorCall) *goja.Object {
	// TODO accept arguments
	// We return a record.Record struct, however because we are
	// not changing call.This instanceof will not work as expected.

	r := record.Record{
		Metadata: make(map[string]string),
	}
	// We need to return a pointer to make the returned object mutable.
	return p.runtime.ToValue(&r).ToObject(p.runtime)
}

func (p *jsProcessor) jsContentRaw(goja.ConstructorCall) *goja.Object {
	// TODO accept arguments
	// We return a record.RawData struct, however because we are
	// not changing call.This instanceof will not work as expected.

	r := record.RawData{}
	// We need to return a pointer to make the returned object mutable.
	return p.runtime.ToValue(&r).ToObject(p.runtime)
}

func (p *jsProcessor) Execute(_ context.Context, in record.Record) (record.Record, error) {
	jsRecord := p.toJSRecord(in)

	result, err := p.function(goja.Undefined(), jsRecord)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to transform to JS record: %w", err)
	}

	out, err := p.toInternal(result)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to transform to internal record: %w", err)
	}

	// nil will be returned if the JS function has no return value at all
	if out == nil {
		return record.Record{}, processor.ErrSkipRecord
	}

	return *out, nil
}

func (p *jsProcessor) toJSRecord(r record.Record) goja.Value {
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
	return p.runtime.ToValue(&r)
}

func (p *jsProcessor) toInternal(v goja.Value) (*record.Record, error) {
	r := v.Export()

	switch v := r.(type) {
	case *record.Record:
		return p.dereferenceContent(v), nil
	case nil:
		return nil, nil
	default:
		return nil, cerrors.Errorf("js function expected to return a Record, but returned: %T", v)
	}
}

func (p *jsProcessor) dereferenceContent(r *record.Record) *record.Record {
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

	return r
}
