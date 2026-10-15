// Copyright © 2026 Meroxa, Inc.
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

package plugin

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

func TestNewBuiltinSchemaUnscopedError(t *testing.T) {
	is := is.New(t)
	err := NewBuiltinSchemaUnscopedError("get", "orders")
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeBuiltinSchemaUnscoped)
	is.Equal(ce.Message, `built-in plugin schema get for subject "orders" has no runtime scope in its context`)
	is.True(ce.Suggestion != "")
}
