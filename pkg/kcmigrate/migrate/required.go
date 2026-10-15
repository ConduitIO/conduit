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

package migrate

import (
	"sort"
	"strings"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
)

// BuiltinRequired returns the required settings of a built-in connector's
// source or destination, from the specification compiled into the binary.
// A plugin that is not built in (a standalone connector) has none listed
// here; its family's rules name what it needs.
func BuiltinRequired(plugin, role string) []string {
	name := strings.TrimPrefix(plugin, "builtin:")
	if name == plugin {
		return nil
	}
	for _, c := range builtin.DefaultBuiltinConnectors {
		spec := c.NewSpecification()
		if spec.Name != name {
			continue
		}
		params := spec.SourceParams
		if role == "destination" {
			params = spec.DestinationParams
		}
		var out []string
		for k, p := range params {
			for _, v := range p.Validations {
				if v.Type() == config.ValidationTypeRequired {
					out = append(out, k)
				}
			}
		}
		sort.Strings(out)
		return out
	}
	return nil
}
