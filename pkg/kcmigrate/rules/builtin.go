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
	"embed"
	"io/fs"
	"sort"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

//go:embed data
var data embed.FS

var builtin = sync.OnceValues(func() (*Engine, error) {
	sub, err := fs.Sub(data, "data")
	if err != nil {
		return nil, cerrors.Errorf("rule data: %w", err)
	}
	return Load(sub)
})

// Builtin returns the engine for the rule files embedded in the binary
// (rules/data). The files are loaded once; an error means the embedded data
// is invalid, which TestBuiltin catches before release.
func Builtin() (*Engine, error) { return builtin() }

// Classes returns the connector classes the engine has rules for, sorted.
func (e *Engine) Classes() []string {
	out := make([]string, 0, len(e.classes))
	for c := range e.classes {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// ActionRef is one emit action in the rule data, for checks that every
// action names a kind the emitters implement.
type ActionRef struct {
	File, Key string
	Action    Action
}

// Actions lists every emit action of every loaded rule file, in file and
// rule order.
func (e *Engine) Actions() []ActionRef {
	var out []ActionRef
	for _, f := range e.files {
		for _, r := range f.Keys {
			if r.Emit != nil {
				out = append(out, ActionRef{File: f.name, Key: r.Key.String(), Action: *r.Emit})
			}
			for _, b := range r.Values {
				if b.Emit != nil {
					out = append(out, ActionRef{File: f.name, Key: r.Key.String(), Action: *b.Emit})
				}
			}
		}
	}
	return out
}
