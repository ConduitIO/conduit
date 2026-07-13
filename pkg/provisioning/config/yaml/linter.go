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
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/provisioning/config"
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
		return cl.newWarning(path, node, c), true
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

// newWarning builds the warning for a changelog match at path (the full,
// document-rooted traversal path the decoder hook reports — real slice
// indices, not wildcards; see decode.go's triggerHook) against node (the
// field's current value node).
//
// When c is a pure rename (RenamedTo != "", the repair v1 starter set's
// item #1 — see internal.Change.RenamedTo's doc), the warning additionally
// carries a machine-appliable conduiterr.Fix and the dedicated
// config.CodeFieldRenamed code, so cmd/conduit/internal/repair can offer it
// as a fix. The Fix is a deliberate, documented special case of the
// {ConfigPath,Op,Value} shape (design doc §3.1's "compound Fix bundle"):
// ConfigPath is the OLD field's full JSON pointer (path, joined), Op is
// "remove" (the literal action against that path), and Value carries the
// NEW field's key name rather than a value to set — repair's compound
// registry (keyed on config.CodeFieldRenamed) reads Value as "the sibling
// key to add", not as an add/set payload, and copies the old node's current
// value into it. This keeps the wire Fix within the frozen three-op model
// without inventing a fourth "rename" op or a []Fix field (see the design
// doc's alternatives-considered §11).
func (cl *configLinter) newWarning(path []string, node *yaml.Node, c internal.Change) warning {
	w := warning{
		field:   path[len(path)-1],
		line:    node.Line,
		column:  node.Column,
		value:   node.Value,
		message: c.Message,
	}
	if c.RenamedTo != "" {
		w.code = config.CodeFieldRenamed.Reason()
		w.fix = &conduiterr.Fix{
			ConfigPath: "/" + strings.Join(path, "/"),
			Op:         "remove",
			Value:      c.RenamedTo,
		}
	}
	return w
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
	// code and fix are set only for a rename-class warning (see newWarning);
	// every other warning leaves both zero, exactly as before this field was
	// added.
	code string
	fix  *conduiterr.Fix
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
