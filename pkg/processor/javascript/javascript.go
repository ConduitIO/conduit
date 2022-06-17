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
func (e Function) Call(r record.Record) (interface{}, error) {
	result, err := e.function(goja.Undefined(), e.runtime.ToValue(r))
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to transform to JS record: %w", err)
	}

	out := result.Export()
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to transform to internal record: %w", err)
	}

	return out, nil
}

func setRuntimeHelpers(logger zerolog.Logger, rt *goja.Runtime) error {
	// todo add more, merge with the ones from transform
	runtimeHelpers := map[string]interface{}{
		"logger": &logger,
	}

	for name, helper := range runtimeHelpers {
		if err := rt.Set(name, helper); err != nil {
			return cerrors.Errorf("failed to set helper %q: %w", name, err)
		}
	}
	return nil
}
