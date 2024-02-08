// Copyright © 2024 Meroxa, Inc.
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

package builtin

import (
	"context"
	"os"
	"sync"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/require"
)

const entrypoint = "process"

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

// gojaContext represents one independent goja context.
type gojaContext struct {
	runtime  *goja.Runtime
	function goja.Callable
}

type jsProcessor struct {
	sdk.UnimplementedProcessor

	// src is the JavaScript code that will be executed
	src string

	gojaPool sync.Pool
	logger   log.CtxLogger
}

func newJsProcessor(logger log.CtxLogger) *jsProcessor {
	return &jsProcessor{logger: logger}
}

func (p *jsProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "custom.javascript",
		Summary:     "",
		Description: "",
		Version:     "",
		Author:      "",
		Parameters: map[string]sdk.Parameter{
			"script": {
				Default:     "",
				Type:        sdk.ParameterTypeString,
				Description: "Processor code.",
				Validations: nil,
			},
			"script.path": {
				Default:     "",
				Type:        sdk.ParameterTypeString,
				Description: "Path to a .js file containing the processor code.",
				Validations: nil,
			},
		},
	}, nil
}

func (p *jsProcessor) Configure(_ context.Context, m map[string]string) error {
	script, hasScript := m["script"]
	scriptPath, hasScriptPath := m["script.path"]
	switch {
	case hasScript && hasScriptPath:
		return cerrors.New("only one of: [script, script.path] should be provided")
	case hasScript:
		p.src = script
	case hasScriptPath:
		file, err := os.ReadFile(scriptPath)
		if err != nil {
			return cerrors.Errorf("error reading script from path %v: %w", scriptPath, err)
		}
		p.src = string(file)
	}

	return nil
}

func (p *jsProcessor) Open(context.Context) error {
	runtime, err := p.newJSRuntime(p.logger)
	if err != nil {
		return cerrors.Errorf("failed initializing JS runtime: %w", err)
	}

	_, err = p.newFunction(runtime, p.src)
	if err != nil {
		return cerrors.Errorf("failed initializing JS function: %w", err)
	}

	p.gojaPool.New = func() any {
		// create a new runtime for the function, so it's executed in a separate goja context
		rt, _ := p.newJSRuntime(p.logger)
		f, _ := p.newFunction(rt, p.src)
		return &gojaContext{
			runtime:  rt,
			function: f,
		}
	}

	return nil
}

func (p *jsProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	g := p.gojaPool.Get().(*gojaContext)
	defer p.gojaPool.Put(g)

	out := make([]sdk.ProcessedRecord, len(records))
	for i, in := range records {
		out[i] = p.process(g, in)
	}

	return out
}

func (p *jsProcessor) Teardown(context.Context) error {
	return nil
}

func (p *jsProcessor) newJSRuntime(logger log.CtxLogger) (*goja.Runtime, error) {
	rt := goja.New()
	require.NewRegistry().Enable(rt)

	runtimeHelpers := map[string]interface{}{
		"logger":         &logger,
		"Record":         p.jsRecord(rt),
		"RawData":        p.jsContentRaw(rt),
		"StructuredData": p.jsContentStructured(rt),
	}

	for name, helper := range runtimeHelpers {
		if err := rt.Set(name, helper); err != nil {
			return nil, cerrors.Errorf("failed to set helper %q: %w", name, err)
		}
	}

	return rt, nil
}

func (p *jsProcessor) newFunction(runtime *goja.Runtime, src string) (goja.Callable, error) {
	prg, err := goja.Compile("", src, false)
	if err != nil {
		return nil, cerrors.Errorf("failed to compile script: %w", err)
	}

	_, err = runtime.RunProgram(prg)
	if err != nil {
		return nil, cerrors.Errorf("failed to run program: %w", err)
	}

	tmp := runtime.Get(entrypoint)
	entrypointFunc, ok := goja.AssertFunction(tmp)
	if !ok {
		return nil, cerrors.Errorf("failed to get entrypoint function %q", entrypoint)
	}

	return entrypointFunc, nil
}

func (p *jsProcessor) jsRecord(runtime *goja.Runtime) func(goja.ConstructorCall) *goja.Object {
	return func(call goja.ConstructorCall) *goja.Object {
		// TODO accept arguments
		// We return a jsRecord struct, however because we are
		// not changing call.This instanceof will not work as expected.

		r := jsRecord{
			Metadata: make(map[string]string),
		}
		// We need to return a pointer to make the returned object mutable.
		return runtime.ToValue(&r).ToObject(runtime)
	}
}

func (p *jsProcessor) jsContentRaw(runtime *goja.Runtime) func(goja.ConstructorCall) *goja.Object {
	return func(call goja.ConstructorCall) *goja.Object {
		// TODO accept arguments
		// We return a record.RawData struct, however because we are
		// not changing call.This instanceof will not work as expected.

		r := record.RawData{}
		// We need to return a pointer to make the returned object mutable.
		return runtime.ToValue(&r).ToObject(runtime)
	}
}

func (p *jsProcessor) jsContentStructured(runtime *goja.Runtime) func(goja.ConstructorCall) *goja.Object {
	return func(call goja.ConstructorCall) *goja.Object {
		// TODO accept arguments
		// We return a map[string]interface{} struct, however because we are
		// not changing call.This instanceof will not work as expected.

		r := make(map[string]interface{})
		return runtime.ToValue(r).ToObject(runtime)
	}
}

func (p *jsProcessor) toJSRecord(runtime *goja.Runtime, r opencdc.Record) goja.Value {
	convertData := func(d opencdc.Data) interface{} {
		switch v := d.(type) {
		case opencdc.RawData:
			return &v
		case opencdc.StructuredData:
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
	return runtime.ToValue(&jsr)
}

func (p *jsProcessor) toInternalRecord(v goja.Value) sdk.ProcessedRecord {
	raw := v.Export()
	if raw == nil {
		return sdk.FilterRecord{}
	}

	jsr, ok := v.Export().(*jsRecord)
	if !ok {
		return sdk.ErrorRecord{
			Error: cerrors.Errorf("js function expected to return %T, but returned: %T", &jsRecord{}, v),
		}
	}

	var op opencdc.Operation
	err := op.UnmarshalText([]byte(jsr.Operation))
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("could not unmarshal operation: %w", err)}
	}

	convertData := func(d interface{}) opencdc.Data {
		switch v := d.(type) {
		case *opencdc.RawData:
			return *v
		case map[string]interface{}:
			return opencdc.StructuredData(v)
		}
		return nil
	}

	return sdk.SingleRecord{
		Position:  jsr.Position,
		Operation: op,
		Metadata:  jsr.Metadata,
		Key:       convertData(jsr.Key),
		Payload: opencdc.Change{
			Before: convertData(jsr.Payload.Before),
			After:  convertData(jsr.Payload.After),
		},
	}
}

func (p *jsProcessor) process(gojaCtx *gojaContext, in opencdc.Record) sdk.ProcessedRecord {
	jsr := p.toJSRecord(gojaCtx.runtime, in)

	result, err := gojaCtx.function(goja.Undefined(), jsr)
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("failed to execute JS processor function: %w", err)}
	}

	return p.toInternalRecord(result)
}
