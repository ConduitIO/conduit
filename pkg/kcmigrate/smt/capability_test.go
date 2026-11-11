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

package smt_test

import (
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
)

// TestCapabilities_CoverEmittedPlugins: every plugin a rule file emits has
// a capability row for its role, so a new connector family states what its
// plugins do with headers, partitions and topics before an SMT reaches it.
func TestCapabilities_CoverEmittedPlugins(t *testing.T) {
	e, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	d := data(t)
	for _, em := range e.Emits() {
		if !d.HasCapability(em.Plugin, em.Role) {
			t.Errorf("%s %s has no capability row in pkg/kcmigrate/smt/data.go", em.Plugin, em.Role)
		}
	}
}

// TestCapabilities_ResolveAgainstPluginSpecs: a topic setting the table
// names is a parameter of the plugin's specification at the version
// compiled in, so a connector bump that renames it fails here instead of
// writing a setting the plugin ignores.
func TestCapabilities_ResolveAgainstPluginSpecs(t *testing.T) {
	checked := 0
	for _, c := range data(t).Capabilities() {
		if c.TopicSetting == "" {
			continue
		}
		name := strings.TrimPrefix(c.Plugin, "builtin:")
		found := false
		for _, bc := range builtin.DefaultBuiltinConnectors {
			spec := bc.NewSpecification()
			if spec.Name != name {
				continue
			}
			found = true
			params := spec.SourceParams
			if c.Role == "destination" {
				params = spec.DestinationParams
			}
			if _, ok := params[c.TopicSetting]; !ok {
				t.Errorf("%s %s has no parameter %s", c.Plugin, c.Role, c.TopicSetting)
			}
			checked++
		}
		if !found {
			t.Errorf("no built-in connector %s", c.Plugin)
		}
	}
	if checked == 0 {
		t.Fatal("no topic setting was checked")
	}
}
