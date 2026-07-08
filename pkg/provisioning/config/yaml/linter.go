// Copyright © 2023 Meroxa, Inc.
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

package yaml

import (
	"context"
	"sort"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/provisioning/config/yaml/internal"
	"github.com/conduitio/yaml/v3"
)

type configLinter struct {
	// expandedChangelog contains a map of all changes in the changelog. The
	// first key is the version, the second is a map of changes in that version.
	// Changes are stored hierarchical in submaps. For example, if the field
	// x.y.z changed in version 1.2.3 the map will contain
	// { "1.2.3" : { "x" : { "y" : { "z" : internal.Change{} } } } }.
	expandedChangelog map[string]map[string]any
}

func newConfigLinter(changelogs ...internal.Changelog) *configLinter {
	// expand changelogs
	expandedChangelog := make(map[string]map[string]any)
	for _, changelog := range changelogs {
		for k, v := range changelog.Expand() {
			if _, ok := expandedChangelog[k]; ok {
				panic(cerrors.Errorf("changelog already contains version %v", k))
			}
			expandedChangelog[k] = v
		}
	}

	return &configLinter{
		expandedChangelog: expandedChangelog,
	}
}

func (cl *configLinter) DecoderHook(version string, warn *Warnings) yaml.DecoderHook {
	return func(path []string, node *yaml.Node) {
		if w, ok := cl.InspectNode(version, path, node); ok {
			*warn = append(*warn, w)
		}
	}
}

func (cl *configLinter) InspectNode(version string, path []string, node *yaml.Node) (Warning, bool) {
	if c, ok := cl.findChange(version, path); ok {
		return cl.newWarning(path[len(path)-1], node, c.Message), true
	}
	return Warning{}, false
}

func (cl *configLinter) findChange(version string, path []string) (internal.Change, bool) {
	curMap := cl.expandedChangelog[version]
	last := len(path) - 1
	for i, field := range path {
		nextMap, ok := curMap[field]
		if !ok {
			nextMap, ok = curMap["*"]
			if !ok {
				break
			}
		}
		switch v := nextMap.(type) {
		case map[string]any:
			curMap = v
			continue
		case internal.Change:
			if i == last {
				return v, true
			}
		}
		break
	}
	return internal.Change{}, false
}

func (cl *configLinter) newWarning(field string, node *yaml.Node, message string) Warning {
	return Warning{
		Field:   field,
		Line:    node.Line,
		Column:  node.Column,
		Value:   node.Value,
		Message: message,
	}
}

// Warnings is a collection of Warning, in the order they were collected by
// the parser. Exported (originally package-private) so callers outside this
// package — currently cmd/conduit/internal/validate, backing the `lint` and
// `dry-run` CLI verbs — can turn them into advisory findings instead of only
// reading them from the log stream. See Parser.ParseWithWarnings.
type Warnings []Warning

// Sort orders w by line number ascending, in place, and returns it for
// chaining. Used identically by both the logging path (ParseConfigurations)
// and the returning path (ParseWithWarnings) so a caller sees the same order
// either way.
func (w Warnings) Sort() Warnings {
	sort.Slice(w, func(i, j int) bool {
		return w[i].Line < w[j].Line
	})
	return w
}

// Log writes every warning in w to logger at Warn level. This is
// `conduit run`'s (via pkg/provisioning.Service) only consumer of parser
// warnings today — ParseConfigurations calls it unconditionally, exactly as
// before this type was exported, so that behavior is unchanged.
func (w Warnings) Log(ctx context.Context, logger log.CtxLogger) {
	for _, ww := range w {
		ww.Log(ctx, logger)
	}
}

// Warning is one advisory parser finding: a deprecated/renamed/unknown field,
// or a version fallback, located at Line/Column with a human-readable
// Message. Exported so cmd/conduit/internal/validate can map it onto a
// validate.Finding with Severity: "warning" — see that package's report.go.
type Warning struct {
	Field   string
	Line    int
	Column  int
	Value   string
	Message string
}

// Log writes w to logger at Warn level, mirroring the JSON shape
// TestParser_V1_Warnings/TestParser_V2_Warnings pin: line, column, field,
// (optional) value, then the message.
func (w Warning) Log(ctx context.Context, logger log.CtxLogger) {
	e := logger.Warn(ctx)
	if w.Line != 0 {
		e = e.Int("line", w.Line)
	}
	if w.Column != 0 {
		e = e.Int("column", w.Column)
	}
	if w.Field != "" {
		e = e.Str("field", w.Field)
	}
	if w.Value != "" {
		e = e.Str("value", w.Value)
	}
	e.Msg(w.Message)
}
