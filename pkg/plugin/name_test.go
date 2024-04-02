// Copyright © 2022 Meroxa, Inc.
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

package plugin

import (
	"fmt"
	"testing"

	"github.com/matryer/is"
)

func TestFullName(t *testing.T) {
	testCases := []struct {
		fn string // full name
		pt string // expected plugin type
		pn string // expected plugin name
		pv string // expected plugin version
	}{
		{fn: "", pt: "any", pn: "", pv: "latest"},
		{fn: "my-plugin", pt: "any", pn: "my-plugin", pv: "latest"},
		{fn: "builtin:my-plugin", pt: "builtin", pn: "my-plugin", pv: "latest"},
		{fn: "my-plugin@v0.1.1", pt: "any", pn: "my-plugin", pv: "v0.1.1"},
		{fn: "standalone:my-plugin@v0.1.1", pt: "standalone", pn: "my-plugin", pv: "v0.1.1"},
		{fn: "random:my-plugin@non-semantic", pt: "random", pn: "my-plugin", pv: "non-semantic"},
	}

	for _, tc := range testCases {
		t.Run(tc.fn, func(t *testing.T) {
			is := is.New(t)
			fn := FullName(tc.fn)
			is.Equal(fn.PluginType(), tc.pt)
			is.Equal(fn.PluginName(), tc.pn)
			is.Equal(fn.PluginVersion(), tc.pv)
		})
	}
}

func TestFullName_PluginVersionGreaterThan(t *testing.T) {
	testCases := []struct {
		v1 string // left version
		v2 string // right version
		gt bool   // is left greater than right
	}{
		{v1: "v0.0.1", v2: "v0.0.1", gt: false},
		{v1: "v0.1", v2: "v0.0.1", gt: true},
		{v1: "v0.0.1", v2: "v0.1.0", gt: false},
		{v1: "v1", v2: "v0.1.0", gt: true},
		{v1: "v1", v2: "v0.1", gt: true},
		{v1: "foo", v2: "v0.0.1", gt: false},
		{v1: "v0.0.1", v2: "foo", gt: true},
		{v1: "foo", v2: "bar", gt: false},
		{v1: "bar", v2: "foo", gt: true},
		{v1: "v0.0.1", v2: "v0.0.1-dirty", gt: true},
		{v1: "zoo", v2: "", gt: true},
		{v1: "v1", v2: "", gt: true},
		{v1: "", v2: "zoo", gt: false},
		{v1: "", v2: "v1", gt: false},
		{v1: "", v2: "", gt: false},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%v/%v", tc.v1, tc.v2), func(t *testing.T) {
			is := is.New(t)
			v1 := NewFullName("builtin", "test", tc.v1)
			v2 := NewFullName("builtin", "test", tc.v2)
			is.Equal(v1.PluginVersionGreaterThan(v2), tc.gt)

			// even if plugin type and plugin name are empty, the version
			// comparison should still work the same
			v1 = NewFullName("", "", tc.v1)
			v2 = NewFullName("", "", tc.v2)
			is.Equal(v1.PluginVersionGreaterThan(v2), tc.gt)
		})
	}
}
