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
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/dop251/goja"
	"github.com/rs/zerolog"
	"strings"
)

const (
	entrypoint = "process"
)

// jsRecord is an intermediary representation of record.Record that is passed to
// the JavaScript transform. We use this because using record.Record would not
// allow us to modify or access certain data (e.g. metadata or structured data).
type jsRecord struct {
	Position  []byte
	Operation string
	Metadata  map[string]string
	Key       any
	Payload   struct {
		Before any
		After  any
	}
}

// Processor is able to run processors defined in JavaScript.
type Processor struct {
	runtime  *goja.Runtime
	function goja.Callable
	inInsp   *inspector.Inspector
	outInsp  *inspector.Inspector
}

func New(src string, logger zerolog.Logger) (*Processor, error) {
	// todo use real logger
	p := &Processor{
		inInsp:  inspector.New(log.New(logger), 1000),
		outInsp: inspector.New(log.New(logger), 1000),
	}
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
		"logger":         &logger,
		"Record":         p.jsRecord,
		"RawData":        p.jsContentRaw,
		"StructuredData": p.jsContentStructured,
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
	// We return a jsRecord struct, however because we are
	// not changing call.This instanceof will not work as expected.

	r := jsRecord{
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

func (p *Processor) jsContentStructured(goja.ConstructorCall) *goja.Object {
	// TODO accept arguments
	// We return a map[string]interface{} struct, however because we are
	// not changing call.This instanceof will not work as expected.

	r := make(map[string]interface{})
	return p.runtime.ToValue(r).ToObject(p.runtime)
}

func (p *Processor) Process(ctx context.Context, in record.Record) (record.Record, error) {
	p.inInsp.Send(ctx, in)
	jsr := p.toJSRecord(in)

	result, err := p.function(goja.Undefined(), jsr)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to execute JS processor function: %w", err)
	}

	out, err := p.toInternalRecord(result)
	if err == processor.ErrSkipRecord {
		return record.Record{}, err
	}
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to transform to internal record: %w", err)
	}

	p.outInsp.Send(ctx, out)
	return out, nil
}

func (p *Processor) Inspect(ctx context.Context, direction string) *inspector.Session {
	switch strings.ToLower(direction) {
	case "in":
		return p.inInsp.NewSession(ctx)
	case "out":
		return p.outInsp.NewSession(ctx)
	default:
		panic("unknown direction: " + direction)
	}
}

func (p *Processor) toJSRecord(r record.Record) goja.Value {
	convertData := func(d record.Data) interface{} {
		switch v := d.(type) {
		case record.RawData:
			return &v
		case record.StructuredData:
			return map[string]interface{}(v)
		}
		return nil
	}

	jsr := jsRecord{
		Position:  r.Position,
		Operation: r.Operation.String(),
		Metadata:  r.Metadata,
		Key:       convertData(r.Key),
		Payload: struct {
			Before interface{}
			After  interface{}
		}{
			Before: convertData(r.Payload.Before),
			After:  convertData(r.Payload.After),
		},
	}

	// we need to send in a pointer to let the user change the value and return it, if they choose to do so
	return p.runtime.ToValue(&jsr)
}

func (p *Processor) toInternalRecord(v goja.Value) (record.Record, error) {
	raw := v.Export()
	if raw == nil {
		return record.Record{}, processor.ErrSkipRecord
	}

	jsr, ok := v.Export().(*jsRecord)
	if !ok {
		return record.Record{}, cerrors.Errorf("js function expected to return %T, but returned: %T", &jsRecord{}, v)
	}

	var op record.Operation
	err := op.UnmarshalText([]byte(jsr.Operation))
	if err != nil {
		return record.Record{}, cerrors.Errorf("could not unmarshal operation: %w", err)
	}

	convertData := func(d interface{}) record.Data {
		switch v := d.(type) {
		case *record.RawData:
			return *v
		case map[string]interface{}:
			return record.StructuredData(v)
		}
		return nil
	}

	return record.Record{
		Position:  jsr.Position,
		Operation: op,
		Metadata:  jsr.Metadata,
		Key:       convertData(jsr.Key),
		Payload: record.Change{
			Before: convertData(jsr.Payload.Before),
			After:  convertData(jsr.Payload.After),
		},
	}, nil
}
