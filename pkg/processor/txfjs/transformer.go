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

package txfjs

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/dop251/goja"
	"github.com/rs/zerolog"
)

const (
	entrypoint = "transform"
)

// Transformer is able to run transformations defined in JavaScript.
type Transformer struct {
	runtime *goja.Runtime
	f       goja.Callable
}

func NewTransformer(src string, logger zerolog.Logger) (*Transformer, error) {
	rt := goja.New()
	err := setRuntimeHelpers(logger, rt)
	if err != nil {
		return nil, err
	}

	prg, err := goja.Compile("", src, false)
	if err != nil {
		return nil, cerrors.Errorf("failed to compile transformer script: %w", err)
	}

	_, err = rt.RunProgram(prg)
	if err != nil {
		return nil, cerrors.Errorf("failed to run program: %w", err)
	}

	tmp := rt.Get(entrypoint)
	entrypointFunc, ok := goja.AssertFunction(tmp)
	if !ok {
		return nil, cerrors.Errorf("failed to get entrypoint function %q", entrypoint)
	}

	return &Transformer{
		runtime: rt,
		f:       entrypointFunc,
	}, nil
}

func (t *Transformer) Transform(in record.Record) (record.Record, error) {
	jsRecord := t.toJSRecord(in)

	result, err := t.f(goja.Undefined(), jsRecord)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to transform to JS record: %w", err)
	}

	// TODO would be nice if we could validate this when creating the transformer
	out, err := t.toInternalRecord(result)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to transform to internal record: %w", err)
	}

	return out, nil
}

func (t *Transformer) toJSRecord(r record.Record) goja.Value {
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
	return t.runtime.ToValue(&r)
}

func (t *Transformer) toInternalRecord(v goja.Value) (record.Record, error) {
	r, ok := v.Export().(*record.Record)
	if !ok {
		return record.Record{}, cerrors.Errorf("unexpected type, expected %T, got %T", r, v.Export())
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
