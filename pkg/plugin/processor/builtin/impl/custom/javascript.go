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

//go:generate paramgen -output=javascript_paramgen.go javascriptConfig

package custom

import (
	"context"
	"os"
	"sync"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/require"
)

const entrypoint = "process"

// jsRecord is an intermediary representation of opencdc.Record that is passed to
// the JavaScript transform. We use this because using opencdc.Record would not
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
type javascriptConfig struct {
	// JavaScript code for this processor.
	// It needs to have a function `process()` that accept
	// a record and returns a record.
	// The `process()` function can either modify the input record and return it,
	// or create a new record.
	// If a record needs to be filtered (dropped from the pipeline),
	// then the `process()` function should return `null`.
	Script string `json:"script"`
	// The path to a .js file containing the processor code.
	ScriptPath string `json:"script.path"`
}
type JavascriptProcessor struct {
	sdk.UnimplementedProcessor

	// src is the JavaScript code that will be executed
	src string

	gojaPool sync.Pool
	logger   log.CtxLogger
}

func NewJavascriptProcessor(logger log.CtxLogger) *JavascriptProcessor {
	return &JavascriptProcessor{logger: logger}
}

func (p *JavascriptProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "custom.javascript",
		Summary: "Run custom JavaScript code.",
		Description: `A processor that makes it possible to process Conduit records using JavaScript.

The following helper functions and variables are available:
* ` + "`logger`" + `: a logger that outputs to Conduit's logs. Check out [Zerolog's API](https://github.com/rs/zerolog) on how to use it.
* ` + "`Record()`" + `: constructs a new record which represents a successful processing result.
It's analogous to ` + "`sdk.SingleRecord`" + ` from Conduit's Go processor SDK.
* ` + "`RawData()`" + `: creates a raw data object. It's analogous to ` + "`opencdc.RawData`" + `. Optionally, it
accepts a string argument, which will be cast into a byte array, for example: ` + "`record.Key = RawData(\"new key\")`" + `.
* ` + "`StructuredData()`" + `: creates a structured data (map-like) object.

To find out what's possible with the JS processor, also refer to the documentation for 
[goja](https://github.com/dop251/goja), which is the JavaScript engine we use.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: javascriptConfig{}.Parameters(),
	}, nil
}

func (p *JavascriptProcessor) Configure(ctx context.Context, c config.Config) error {
	cfg := javascriptConfig{}
	err := sdk.ParseConfig(ctx, c, &cfg, cfg.Parameters())
	if err != nil {
		return cerrors.Errorf("failed parsing configuration: %w", err)
	}

	switch {
	case cfg.Script != "" && cfg.ScriptPath != "":
		return cerrors.New("only one of: [script, script.path] should be provided")
	case cfg.Script != "":
		p.src = cfg.Script
	case cfg.ScriptPath != "":
		file, err := os.ReadFile(cfg.ScriptPath)
		if err != nil {
			return cerrors.Errorf("error reading script from path %v: %w", cfg.ScriptPath, err)
		}
		p.src = string(file)
	default:
		return cerrors.New("one of: [script, script.path] needs to be provided")
	}

	return nil
}

func (p *JavascriptProcessor) Open(context.Context) error {
	runtime, err := p.newRuntime(p.logger)
	if err != nil {
		return cerrors.Errorf("failed initializing JS runtime: %w", err)
	}

	_, err = p.newFunction(runtime, p.src)
	if err != nil {
		return cerrors.Errorf("failed initializing JS function: %w", err)
	}

	p.gojaPool.New = func() any {
		// create a new runtime for the function, so it's executed in a separate goja context
		rt, _ := p.newRuntime(p.logger)
		f, _ := p.newFunction(rt, p.src)
		return &gojaContext{
			runtime:  rt,
			function: f,
		}
	}

	return nil
}

func (p *JavascriptProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	g := p.gojaPool.Get().(*gojaContext)
	defer p.gojaPool.Put(g)

	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		jsRecs := p.toJSRecord(g.runtime, rec)
		result, err := g.function(goja.Undefined(), jsRecs)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}

		proc, err := p.toSDKRecords(result)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}

		out = append(out, proc)
	}

	return out
}

func (p *JavascriptProcessor) Teardown(context.Context) error {
	return nil
}

func (p *JavascriptProcessor) newRuntime(logger log.CtxLogger) (*goja.Runtime, error) {
	rt := goja.New()
	require.NewRegistry().Enable(rt)

	runtimeHelpers := map[string]interface{}{
		"logger":         &logger,
		"Record":         p.newSingleRecord(rt),
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

func (p *JavascriptProcessor) newFunction(runtime *goja.Runtime, src string) (goja.Callable, error) {
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

func (p *JavascriptProcessor) newSingleRecord(runtime *goja.Runtime) func(goja.ConstructorCall) *goja.Object {
	return func(call goja.ConstructorCall) *goja.Object {
		// We return a singleRecord struct, however because we are
		// not changing call.This instanceof will not work as expected.

		// JavaScript records are always initialized with metadata
		// so that it's easier to write processor code
		// (without worrying about initializing it every time)
		r := jsRecord{
			Metadata: make(map[string]string),
		}
		// We need to return a pointer to make the returned object mutable.
		return runtime.ToValue(&r).ToObject(runtime)
	}
}

func (p *JavascriptProcessor) jsContentRaw(runtime *goja.Runtime) func(goja.ConstructorCall) *goja.Object {
	return func(call goja.ConstructorCall) *goja.Object {
		var r opencdc.RawData
		if len(call.Arguments) > 0 {
			r = opencdc.RawData(call.Argument(0).String())
		}
		// We need to return a pointer to make the returned object mutable.
		return runtime.ToValue(&r).ToObject(runtime)
	}
}

func (p *JavascriptProcessor) jsContentStructured(runtime *goja.Runtime) func(goja.ConstructorCall) *goja.Object {
	return func(call goja.ConstructorCall) *goja.Object {
		// TODO accept arguments
		// We return a map[string]interface{} struct, however because we are
		// not changing call.This instanceof will not work as expected.

		r := make(map[string]interface{})
		return runtime.ToValue(r).ToObject(runtime)
	}
}

func (p *JavascriptProcessor) toJSRecord(runtime *goja.Runtime, r opencdc.Record) goja.Value {
	convertData := func(d opencdc.Data) interface{} {
		switch v := d.(type) {
		case opencdc.RawData:
			return &v
		case opencdc.StructuredData:
			return map[string]interface{}(v)
		}
		return nil
	}

	jsRec := &jsRecord{
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
	return runtime.ToValue(jsRec)
}

func (p *JavascriptProcessor) toSDKRecords(v goja.Value) (sdk.ProcessedRecord, error) {
	raw := v.Export()
	if raw == nil {
		return sdk.FilterRecord{}, nil
	}

	jsr, ok := v.Export().(*jsRecord)
	if !ok {
		return nil, cerrors.Errorf("js function expected to return %T, but returned: %T", &jsRecord{}, v)
	}

	var op opencdc.Operation
	err := op.UnmarshalText([]byte(jsr.Operation))
	if err != nil {
		return nil, cerrors.Errorf("could not unmarshal operation: %w", err)
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
	}, nil
}
