// Copyright © 2025 Meroxa, Inc.
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

package reactor

import (
	"fmt"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/tetratelabs/wazero/api"
)

type functionDefinition struct {
	name        string
	paramTypes  []api.ValueType // parameter types
	resultTypes []api.ValueType // result types
}

var (
	mallocFunctionDefinition = functionDefinition{
		name:        "conduit.processor.v1.malloc",
		paramTypes:  []api.ValueType{api.ValueTypeI32}, // i32 (size of the buffer)
		resultTypes: []api.ValueType{api.ValueTypeI32}, // i32 (pointer to the allocated buffer)
	}
	specificationFunctionDefinition = functionDefinition{
		name: "conduit.processor.v1.specification",
		paramTypes: []api.ValueType{
			api.ValueTypeI32, // i32 (pointer to buffer)
			api.ValueTypeI32, // i32 (size of the buffer)
		},
		resultTypes: []api.ValueType{api.ValueTypeI64}, // i64 (pointer and size of the buffer packed in a single i64)
	}
	configureFunctionDefinition = functionDefinition{
		name: "conduit.processor.v1.configure",
		paramTypes: []api.ValueType{
			api.ValueTypeI32, // i32 (pointer to buffer)
			api.ValueTypeI32, // i32 (size of the buffer)
		},
		resultTypes: []api.ValueType{api.ValueTypeI64}, // i64 (pointer and size of the buffer packed in a single i64)
	}
	openFunctionDefinition = functionDefinition{
		name: "conduit.processor.v1.open",
		paramTypes: []api.ValueType{
			api.ValueTypeI32, // i32 (pointer to buffer)
			api.ValueTypeI32, // i32 (size of the buffer)
		},
		resultTypes: []api.ValueType{api.ValueTypeI64}, // i64 (pointer and size of the buffer packed in a single i64)
	}
	processFunctionDefinition = functionDefinition{
		name: "conduit.processor.v1.process",
		paramTypes: []api.ValueType{
			api.ValueTypeI32, // i32 (pointer to buffer)
			api.ValueTypeI32, // i32 (size of the buffer)
		},
		resultTypes: []api.ValueType{api.ValueTypeI64}, // i64 (pointer and size of the buffer packed in a single i64)
	}
	teardownFunctionDefinition = functionDefinition{
		name: "conduit.processor.v1.teardown",
		paramTypes: []api.ValueType{
			api.ValueTypeI32, // i32 (pointer to buffer)
			api.ValueTypeI32, // i32 (size of the buffer)
		},
		resultTypes: []api.ValueType{api.ValueTypeI64}, // i64 (pointer and size of the buffer packed in a single i64)
	}
)

// getExportedFunction retrieves an exported function from the given module
// and checks if it matches the expected function definition. It returns the
// function if it exists and matches the expected definition, or an error if it
// does not exist or does not match the expected definition.
func getExportedFunction(module api.Module, wantFn functionDefinition) (fn api.Function, err error) {
	fn = module.ExportedFunction(wantFn.name)
	var def api.FunctionDefinition

	func() {
		// Recover from panic that occurs if the function does not exist.
		defer func() {
			if recover() != nil {
				fn = nil
				err = cerrors.Errorf("exported function %q does not exist", wantFn.name)
			}
		}()
		def = fn.Definition()
	}()

	if !isValidFunctionDefinition(wantFn, def) {
		return nil, newFunctionDefinitionError(wantFn, def.ParamTypes(), def.ResultTypes())
	}

	return fn, nil
}

func isValidFunctionDefinition(want functionDefinition, got api.FunctionDefinition) bool {
	if len(got.ParamTypes()) != len(want.paramTypes) ||
		len(got.ResultTypes()) != len(want.resultTypes) {
		return false
	}
	for i, typ := range got.ParamTypes() {
		if want.paramTypes[i] != typ {
			return false
		}
	}
	for i, typ := range got.ResultTypes() {
		if want.resultTypes[i] != typ {
			return false
		}
	}

	return true
}

type functionDefinitionError struct {
	expected       functionDefinition
	gotParamTypes  []api.ValueType // parameter types
	gotResultTypes []api.ValueType // result types
}

func newFunctionDefinitionError(expected functionDefinition, gotParamTypes, gotResultTypes []api.ValueType) *functionDefinitionError {
	return &functionDefinitionError{
		expected:       expected,
		gotParamTypes:  gotParamTypes,
		gotResultTypes: gotResultTypes,
	}
}

func (e *functionDefinitionError) Error() string {
	return fmt.Sprintf(
		"exported Wasm function definition mismatch, expected %s, got %s",
		e.formatFunctionDefinition(e.expected.paramTypes, e.expected.resultTypes),
		e.formatFunctionDefinition(e.gotParamTypes, e.gotResultTypes),
	)
}

func (e *functionDefinitionError) formatFunctionDefinition(params []api.ValueType, results []api.ValueType) string {
	var out strings.Builder
	out.WriteString(e.expected.name + "(")
	out.WriteString(e.formatValueTypes(params))
	out.WriteString(")")
	if len(results) > 0 {
		out.WriteString(" -> (")
		out.WriteString(e.formatValueTypes(results))
		out.WriteString(")")
	}
	return out.String()
}

func (e *functionDefinitionError) formatValueTypes(types []api.ValueType) string {
	if len(types) == 0 {
		return ""
	}

	var out string
	for _, typ := range types {
		out += api.ValueTypeName(typ) + ", "
	}
	out = out[:len(out)-2] // Remove the trailing comma and space
	return out
}
