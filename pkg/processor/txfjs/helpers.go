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
