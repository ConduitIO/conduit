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
