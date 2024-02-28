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

//go:generate paramgen -output=config_paramgen.go processorConfig

package js

import (
	"context"
	"os"
	"sync"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/require"
)

const entrypoint = "process"

// singleRecord is an intermediary representation of opencdc.Record that is passed to
// the JavaScript transform. We use this because using opencdc.Record would not
// allow us to modify or access certain data (e.g. metadata or structured data).
type singleRecord struct {
	Position  []byte
	Operation string
	Metadata  map[string]string
	Key       any
	Payload   struct {
		Before any
		After  any
	}
}

type filterRecord struct {
}

type errorRecord struct {
	Error string
}

// gojaContext represents one independent goja context.
type gojaContext struct {
	runtime  *goja.Runtime
	function goja.Callable
}

type processorConfig struct {
	// Script is the JavaScript code for this processor.
	// It needs to have a function 'process()' that accepts
	// an array of records and returns an array of processed records.
	// The processed records in the returned array need to have matching indexes with
	// records in the input array. In other words, for the record at input_array[i]
	// the processed record should be at output_array[i].
	//
	// The processed record can be one of the following:
	// 1. a processed record itself
	// 2. a filter record (constructed with new FilterRecord())
	// 3. an error record (constructred with new ErrorRecord())
	Script string `json:"script"`
	// ScriptPath is the path to a .js file containing the processor code.
	ScriptPath string `json:"script.path"`
}

type processor struct {
	sdk.UnimplementedProcessor

	// src is the JavaScript code that will be executed
	src string

	gojaPool sync.Pool
	logger   log.CtxLogger
}

func New(logger log.CtxLogger) sdk.Processor {
	return &processor{logger: logger}
}

func (p *processor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "custom.javascript",
		Summary: "JavaScript processor",
		Description: `A processor that makes it possible to process Conduit records using JavaScript.

The following helper functions and fields are available:
* logger: a logger that outputs to Conduit's logs. Check zerolog's API on how to use it.
* SingleRecord(): constructs a new record which represents a successful processing result.
It's analogous to sdk.SingleRecord from Conduit's Go processor SDK.
* FilterRecord(): returning a FilterRecord from the process() functions means that the
record should be dropped from the pipeline.
* ErrorRecord(): returning an ErrorRecord from the process() function means that there
was an error processing the input record. Details about the error can be provided through a
string argument in the constructor, e.g. ErrorRecord("details about the error").
* RawData(): creates a raw data object. It's analogous to opencdc.RawData. Optionally, it
accepts a string argument, which will be cast into a byte array, for example: record.Key = RawData("new key").
* StructuredData(): creates a structured data (map-like) object.

To find out what's possible with the JS processors, also refer to the documentation for 
[goja](https://github.com/dop251/goja), which is the JavaScript engine we use.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: processorConfig{}.Parameters(),
	}, nil
}

func (p *processor) Configure(ctx context.Context, m map[string]string) error {
	cfg := processorConfig{}
	err := sdk.ParseConfig(ctx, m, &cfg, cfg.Parameters())
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

func (p *processor) Open(context.Context) error {
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

func (p *processor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	g := p.gojaPool.Get().(*gojaContext)
	defer p.gojaPool.Put(g)

	jsRecs := p.toJSRecords(g.runtime, records)

	result, err := g.function(goja.Undefined(), jsRecs)
	if err != nil {
		return p.makeProcessedRecords(sdk.ErrorRecord{Error: err}, len(records))
	}

	return p.toSDKRecords(g.runtime, result, len(records))
}

func (p *processor) Teardown(context.Context) error {
	return nil
}

func (p *processor) newRuntime(logger log.CtxLogger) (*goja.Runtime, error) {
	rt := goja.New()
	require.NewRegistry().Enable(rt)

	runtimeHelpers := map[string]interface{}{
		"logger":         &logger,
		"SingleRecord":   p.newSingleRecord(rt),
		"FilterRecord":   p.newFilterRecord(rt),
		"ErrorRecord":    p.newErrorRecord(rt),
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

func (p *processor) newFunction(runtime *goja.Runtime, src string) (goja.Callable, error) {
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

func (p *processor) newSingleRecord(runtime *goja.Runtime) func(goja.ConstructorCall) *goja.Object {
	return func(call goja.ConstructorCall) *goja.Object {
		// We return a singleRecord struct, however because we are
		// not changing call.This instanceof will not work as expected.

		// JavaScript records are always initialized with metadata
		// so that it's easier to write processor code
		// (without worrying about initializing it every time)
		r := singleRecord{
			Metadata: make(map[string]string),
		}
		// We need to return a pointer to make the returned object mutable.
		return runtime.ToValue(&r).ToObject(runtime)
	}
}

func (p *processor) newFilterRecord(runtime *goja.Runtime) func(goja.ConstructorCall) *goja.Object {
	return func(call goja.ConstructorCall) *goja.Object {
		// We need to return a pointer to make the returned object mutable.
		return runtime.ToValue(&filterRecord{}).ToObject(runtime)
	}
}

func (p *processor) newErrorRecord(runtime *goja.Runtime) func(goja.ConstructorCall) *goja.Object {
	return func(call goja.ConstructorCall) *goja.Object {
		rec := &errorRecord{}
		if len(call.Arguments) > 0 {
			rec.Error = call.Arguments[0].String()
		}
		// We need to return a pointer to make the returned object mutable.
		return runtime.ToValue(rec).ToObject(runtime)
	}
}

func (p *processor) jsContentRaw(runtime *goja.Runtime) func(goja.ConstructorCall) *goja.Object {
	return func(call goja.ConstructorCall) *goja.Object {
		var r opencdc.RawData
		if len(call.Arguments) > 0 {
			r = opencdc.RawData(call.Argument(0).String())
		}
		// We need to return a pointer to make the returned object mutable.
		return runtime.ToValue(&r).ToObject(runtime)
	}
}

func (p *processor) jsContentStructured(runtime *goja.Runtime) func(goja.ConstructorCall) *goja.Object {
	return func(call goja.ConstructorCall) *goja.Object {
		// TODO accept arguments
		// We return a map[string]interface{} struct, however because we are
		// not changing call.This instanceof will not work as expected.

		r := make(map[string]interface{})
		return runtime.ToValue(r).ToObject(runtime)
	}
}

func (p *processor) toJSRecords(runtime *goja.Runtime, recs []opencdc.Record) goja.Value {
	convertData := func(d opencdc.Data) interface{} {
		switch v := d.(type) {
		case opencdc.RawData:
			return &v
		case opencdc.StructuredData:
			return map[string]interface{}(v)
		}
		return nil
	}

	jsRecs := make([]*singleRecord, len(recs))
	for i, r := range recs {
		jsRecs[i] = &singleRecord{
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
	}

	// we need to send in a pointer to let the user change the value and return it, if they choose to do so
	return runtime.ToValue(jsRecs)
}

func (p *processor) toSDKRecords(runtime *goja.Runtime, v goja.Value, recordsCount int) []sdk.ProcessedRecord {
	var jsRecs []interface{}
	err := runtime.ExportTo(v, &jsRecs)
	if err != nil {
		return p.makeProcessedRecords(
			sdk.ErrorRecord{Error: cerrors.Errorf("failed exporting JavaScript records to Go values: %w", err)},
			recordsCount,
		)
	}

	out := make([]sdk.ProcessedRecord, 0, len(jsRecs))
	for _, jsr := range jsRecs {
		procRec := p.toProcessedRecord(jsr)
		out = append(out, procRec)
		if _, ok := procRec.(sdk.ErrorRecord); ok {
			return out
		}
	}

	return out
}

func (p *processor) makeProcessedRecords(record sdk.ProcessedRecord, count int) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, count)
	for i := 0; i < count; i++ {
		out[i] = record
	}

	return out
}

func (p *processor) toProcessedRecord(obj interface{}) sdk.ProcessedRecord {
	switch v := obj.(type) {
	case *singleRecord:
		return p.toSingleRecord(v)
	case *filterRecord:
		return sdk.FilterRecord{}
	case *errorRecord:
		return sdk.ErrorRecord{Error: cerrors.New(v.Error)}
	default:
		return sdk.ErrorRecord{
			Error: cerrors.Errorf("expected one of [*singleRecord, *filterRecord, *errorRecord], but got %T", obj),
		}
	}
}

func (p *processor) toSingleRecord(jsRec *singleRecord) sdk.ProcessedRecord {
	var op opencdc.Operation
	err := op.UnmarshalText([]byte(jsRec.Operation))
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
		Position:  jsRec.Position,
		Operation: op,
		Metadata:  jsRec.Metadata,
		Key:       convertData(jsRec.Key),
		Payload: opencdc.Change{
			Before: convertData(jsRec.Payload.Before),
			After:  convertData(jsRec.Payload.After),
		},
	}
}
