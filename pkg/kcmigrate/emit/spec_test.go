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

package emit

import (
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
)

// builtinParams returns the parameter names of a built-in connector's
// source or destination, from the specification compiled into this binary.
func builtinParams(t *testing.T, plugin, role string) (map[string]bool, bool) {
	t.Helper()
	name := strings.TrimPrefix(plugin, "builtin:")
	if name == plugin {
		return nil, false // standalone plugins are checked against pinned specs (later lanes)
	}
	for _, c := range builtin.DefaultBuiltinConnectors {
		spec := c.NewSpecification()
		if spec.Name != name {
			continue
		}
		params := spec.SourceParams
		if role == SideDestination {
			params = spec.DestinationParams
		}
		out := map[string]bool{}
		for k := range params {
			out[k] = true
		}
		return out, true
	}
	t.Fatalf("no built-in connector %s", plugin)
	return nil, false
}

// settingsOf lists the settings an action writes.
func settingsOf(a rules.Action) []string {
	switch a.Kind {
	case KindSetting, KindList:
		return []string{a.Args["name"]}
	case KindTLS:
		return []string{"tls.enabled"}
	case KindSASLJAAS:
		return []string{"saslUsername", "saslPassword"}
	default:
		return nil
	}
}

// TestActions_ResolveAgainstPluginSpecs is the rule-data resolution test of
// the mapping design: every setting an embedded rule emits exists in the
// specification of the plugin it lands on, at the version compiled in. A
// connector lane that renames a setting breaks this test, by design.
func TestActions_ResolveAgainstPluginSpecs(t *testing.T) {
	e, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, ref := range e.Actions() {
		type target struct{ plugin, role string }
		var targets []target
		switch side := ref.Action.Args["side"]; side {
		case SideKafka:
			targets = []target{{kafkaPlugin, SideSource}, {kafkaPlugin, SideDestination}}
		case SideProducer:
			targets = []target{{kafkaPlugin, SideDestination}}
		case SideConsumer:
			targets = []target{{kafkaPlugin, SideSource}}
		case SideSource, SideDestination:
			for _, em := range ref.Emits {
				if em.Role == side {
					targets = append(targets, target{em.Plugin, side})
				}
			}
		}
		for _, tg := range targets {
			params, ok := builtinParams(t, tg.plugin, tg.role)
			if !ok {
				continue
			}
			for _, s := range settingsOf(ref.Action) {
				checked++
				if !params[s] {
					t.Errorf("%s: key %s emits %s on the %s %s, which has no such parameter", ref.File, ref.Key, s, tg.plugin, tg.role)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no setting was checked; the walk is broken")
	}
}
