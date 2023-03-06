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
	"fmt"
	"sort"

	"github.com/Masterminds/semver/v3"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/provisioning/config/yaml/internal"
	"github.com/conduitio/yaml/v3"
)

type configLinter struct {
	changelog internal.Changelog

	expandedChangelog map[string]map[string]any
	version           string
	warnings          warnings
}

func newConfigLinter(changelog internal.Changelog) *configLinter {
	cl := &configLinter{
		changelog: changelog,
	}
	cl.init()
	return cl
}

func (cl *configLinter) init() {
	cl.expandedChangelog = cl.changelog.Expand()

	var versions semver.Collection
	for k := range cl.changelog {
		versions = append(versions, semver.MustParse(k))
	}
	sort.Sort(versions)

	// latest version is the default version
	cl.version = versions[len(versions)-1].Original()
}

func (cl *configLinter) InspectNode(path []string, node *yaml.Node) {
	if len(path) == 1 && path[0] == "version" {
		// version gets special treatment, it adjusts the warning we create
		if _, ok := cl.expandedChangelog[node.Value]; !ok {
			cl.addWarning(path[0], node, fmt.Sprintf("unrecognized version, using parser version %v instead", cl.version))
			return
		}
		cl.version = node.Value
		return
	}

	if c, ok := cl.findChange(path); ok {
		cl.addWarning(path[len(path)-1], node, c.Message)
	}
}

func (cl *configLinter) findChange(path []string) (internal.Change, bool) {
	curMap := cl.expandedChangelog[cl.version]
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

func (cl *configLinter) addWarning(field string, node *yaml.Node, message string) {
	cl.warnings = append(cl.warnings, warning{
		field:   field,
		line:    node.Line,
		column:  node.Column,
		value:   node.Value,
		message: message,
	})
}

func (cl *configLinter) Warnings() warnings {
	return cl.warnings
}

type warnings []warning

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
	e := logger.Warn(ctx).
		Int("line", w.line).
		Int("column", w.column).
		Str("field", w.field).
		Str("value", w.value)
	e.Msg(w.message)
}
