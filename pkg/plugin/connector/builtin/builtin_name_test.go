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

package builtin

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/matryer/is"
)

// The classic engine's default destination batching is keyed on these names
// (pkg/connector/classic_batching.go). If a connector's spec name changes, the
// default silently stops applying; this test catches it.
func TestRegistry_DispenserReportsBuiltinPluginName(t *testing.T) {
	registry := NewRegistry(log.Nop(), DefaultBuiltinConnectors, nil)
	registry.Init(context.Background())
	for _, name := range []string{"postgres", "kafka", "builtin:postgres", "builtin:kafka@latest"} {
		is := is.New(t)
		d, err := registry.NewDispenser(log.Nop(), plugin.FullName(name), pconnector.PluginConfig{})
		is.NoErr(err)

		b, ok := d.(connector.BuiltinDispenser)
		is.True(ok)
		got, ok := b.BuiltinPluginName()
		is.True(ok)
		want := plugin.FullName(name).PluginName()
		is.Equal(got, want)

		// Also through the cleanup wrapper.
		w, ok := connector.DispenserWithCleanup(d, func() {}).(connector.BuiltinDispenser)
		is.True(ok)
		got, ok = w.BuiltinPluginName()
		is.True(ok)
		is.Equal(got, want)
	}
}
