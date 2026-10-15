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

package rules

import (
	"testing"

	"github.com/matryer/is"
)

// TestBuiltin loads the embedded production rule data: invalid rule data is
// a build failure, never a runtime surprise.
func TestBuiltin(t *testing.T) {
	is := is.New(t)
	e, err := Builtin()
	is.NoErr(err)
	is.True(e != nil)
	is.Equal(loadValid(t).Classes(), []string{exampleClass})
}
