// Copyright © 2024 Meroxa, Inc.
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
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"testing"

	"github.com/matryer/is"
)

func TestRegistry_InitList(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	underTest := NewRegistry(log.Nop(), DefaultBuiltinConnectors, nil)
	underTest.Init(ctx)

	specs := underTest.List()

	is.Equal(len(DefaultBuiltinConnectors), len(specs))
	for _, gotSpec := range specs {
		wantSpec := DefaultBuiltinConnectors["github.com/conduitio/conduit-connector-"+gotSpec.Name].NewSpecification()
		is.Equal(
			"",
			cmp.Diff(
				pconnector.Specification(wantSpec),
				gotSpec,
				cmpopts.IgnoreFields(pconnector.Specification{}, "SourceParams", "DestinationParams"),
			),
		)
	}
}

func TestRegistry_NewDispenser_PluginNotFound(t *testing.T) {
	testCases := []struct {
		name       string
		pluginName string
		wantErr    bool
	}{
		{
			name:       "non-existing plugin",
			pluginName: "builtin:foobar",
			wantErr:    true,
		},
		{
			name:       "plugin exists, version doesn't",
			pluginName: "builtin:file@v12.34.56",
			wantErr:    true,
		},
		{
			name:       "existing plugin, no builtin prefix, no version",
			pluginName: "file",
			wantErr:    false,
		},
		{
			name:       "existing plugin, with builtin prefix, no version",
			pluginName: "builtin:file",
			wantErr:    false,
		},
		{
			name: "existing plugin, with builtin prefix, with version",
			pluginName: func() string {
				v := DefaultBuiltinConnectors["github.com/conduitio/conduit-connector-file"].
					NewSpecification().
					Version
				return "builtin:file@" + v
			}(),
			wantErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			underTest := NewRegistry(log.Nop(), DefaultBuiltinConnectors, nil)
			underTest.Init(ctx)

			dispenser, err := underTest.NewDispenser(log.Nop(), plugin.FullName(tc.pluginName), pconnector.PluginConfig{})
			if tc.wantErr {
				is.True(cerrors.Is(err, plugin.ErrPluginNotFound))
				is.True(dispenser == nil)
				return
			}

			is.NoErr(err)
			is.True(dispenser != nil)
		})
	}

}
