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

package procjs

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

// Processor is able to run processors defined in JavaScript.
type Processor struct {
	runtime  *goja.Runtime
	function goja.Callable
}

func New(src string, logger zerolog.Logger) (*Processor, error) {
	p := &Processor{}
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

func (p *Processor) initJSRuntime(logger zerolog.Logger) error {
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

func (p *Processor) initFunction(src string) error {
	prg, err := goja.Compile("", src, false)
	if err != nil {
		return cerrors.Errorf("failed to compile script: %w", err)
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

func (p *Processor) jsRecord(goja.ConstructorCall) *goja.Object {
	// TODO accept arguments
	// We return a record.Record struct, however because we are
	// not changing call.This instanceof will not work as expected.

	r := record.Record{
		Metadata: make(map[string]string),
	}
	// We need to return a pointer to make the returned object mutable.
	return p.runtime.ToValue(&r).ToObject(p.runtime)
}

func (p *Processor) jsContentRaw(goja.ConstructorCall) *goja.Object {
	// TODO accept arguments
	// We return a record.RawData struct, however because we are
	// not changing call.This instanceof will not work as expected.

	r := record.RawData{}
	// We need to return a pointer to make the returned object mutable.
	return p.runtime.ToValue(&r).ToObject(p.runtime)
}

func (p *Processor) Process(_ context.Context, in record.Record) (record.Record, error) {
	jsRecord := p.toJSRecord(in)

	result, err := p.function(goja.Undefined(), jsRecord)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to execute JS processor function: %w", err)
	}

	out, err := p.toInternalRecord(result)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to transform to internal record: %w", err)
	}

	// nil will be returned if the JS function has no return value at all
	if out == nil {
		return record.Record{}, processor.ErrSkipRecord
	}

	return *out, nil
}

func (p *Processor) toJSRecord(r record.Record) goja.Value {
	// convert content to pointers to make it mutable
	switch v := r.Payload.Before.(type) {
	case record.RawData:
		r.Payload.Before = &v
	case record.StructuredData:
		r.Payload.Before = &v
	}

	switch v := r.Payload.After.(type) {
	case record.RawData:
		r.Payload.After = &v
	case record.StructuredData:
		r.Payload.After = &v
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

func (p *Processor) toInternalRecord(v goja.Value) (*record.Record, error) {
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

func (p *Processor) dereferenceContent(r *record.Record) *record.Record {
	// dereference content pointers
	switch v := r.Payload.Before.(type) {
	case *record.RawData:
		r.Payload.Before = *v
	case *record.StructuredData:
		r.Payload.Before = *v
	}

	switch v := r.Payload.After.(type) {
	case *record.RawData:
		r.Payload.After = *v
	case *record.StructuredData:
		r.Payload.After = *v
	}

	switch v := r.Key.(type) {
	case *record.RawData:
		r.Key = *v
	case *record.StructuredData:
		r.Key = *v
	}

	return r
}
