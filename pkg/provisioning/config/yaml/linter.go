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

func (cl *configLinter) DecoderHook(version string, warn *warnings) yaml.DecoderHook {
	return func(path []string, node *yaml.Node) {
		if w, ok := cl.InspectNode(version, path, node); ok {
			*warn = append(*warn, w)
		}
	}
}

func (cl *configLinter) InspectNode(version string, path []string, node *yaml.Node) (warning, bool) {
	if c, ok := cl.findChange(version, path); ok {
		return cl.newWarning(path[len(path)-1], node, c.Message), true
	}
	return warning{}, false
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

func (cl *configLinter) newWarning(field string, node *yaml.Node, message string) warning {
	return warning{
		field:   field,
		line:    node.Line,
		column:  node.Column,
		value:   node.Value,
		message: message,
	}
}

type warnings []warning

func (w warnings) Sort() warnings {
	sort.Slice(w, func(i, j int) bool {
		return w[i].line < w[j].line
	})
	return w
}

func (w warnings) Log(ctx context.Context, logger log.CtxLogger) {
	for _, ww := range w {
		ww.Log(ctx, logger)
	}
}

type warning struct {
	field   string
	line    int
	column  int
	value   string
	message string
}

func (w warning) Log(ctx context.Context, logger log.CtxLogger) {
	e := logger.Warn(ctx)
	if w.line != 0 {
		e = e.Int("line", w.line)
	}
	if w.column != 0 {
		e = e.Int("column", w.column)
	}
	if w.field != "" {
		e = e.Str("field", w.field)
	}
	if w.value != "" {
		e = e.Str("value", w.value)
	}
	e.Msg(w.message)
}
