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
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeBuiltinSchemaUnscoped is raised when a built-in connector or processor
// calls the SDK schema service (schema.Get or schema.Create) with a context
// that was not derived from a call Conduit made into that plugin, so Conduit
// cannot tell which runtime's schema registry the call belongs to. The call is
// refused rather than answered from another runtime's registry. It indicates a
// bug in the plugin (for example a goroutine started with context.Background()
// that resolves schemas): pass the context Conduit hands to Open, Read, Write
// or Process down to schema calls. Internal: a plugin bug, not a configuration
// problem.
var CodeBuiltinSchemaUnscoped = conduiterr.Register("plugin.builtin_schema_unscoped", codes.Internal)

// NewBuiltinSchemaUnscopedError returns the CodeBuiltinSchemaUnscoped error for
// the given schema operation ("get" or "create") of the given subject.
func NewBuiltinSchemaUnscopedError(op, subject string) error {
	err := conduiterr.New(
		CodeBuiltinSchemaUnscoped,
		fmt.Sprintf("built-in plugin schema %s for subject %q has no runtime scope in its context", op, subject),
	)
	err.Suggestion = "pass the context Conduit gives the plugin method down to the schema call; do not resolve schemas from a context.Background() goroutine"
	return err
}
