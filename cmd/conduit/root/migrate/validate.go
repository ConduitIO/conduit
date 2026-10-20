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
	"context"
	"path"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/internal/validate"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	procbuiltin "github.com/conduitio/conduit/pkg/plugin/processor/builtin"
)

// ValidatePipeline runs the `pipelines validate --resolve-plugins` engine
// over one generated pipeline file. migrate.Run calls it for every pipeline
// it writes: a generated file that does not validate is a translator bug,
// never a pipeline handed to the user. The MCP tool passes the same func.
func ValidatePipeline(ctx context.Context) func(name string, content []byte) error {
	plugins := map[string]struct{}{}
	for full := range builtin.DefaultBuiltinConnectors {
		plugins[full] = struct{}{}
		plugins[strings.TrimPrefix(path.Base(full), "conduit-connector-")] = struct{}{}
	}
	processors := map[string]struct{}{}
	for name := range procbuiltin.DefaultBuiltinProcessors {
		processors[name] = struct{}{}
	}
	opts := validate.Options{ResolvePlugins: true, BuiltinPlugins: plugins, BuiltinProcessors: processors}
	return func(name string, content []byte) error {
		rep := validate.RunBytes(ctx, name, content, opts)
		if rep.OK() {
			return nil
		}
		var msgs []string
		for _, f := range rep.Files {
			for _, fd := range f.Findings {
				if fd.Severity == validate.SeverityError {
					msgs = append(msgs, fd.Code+" at "+fd.ConfigPath+": "+fd.Message)
				}
			}
		}
		return cerrors.New(strings.Join(msgs, "; "))
	}
}
