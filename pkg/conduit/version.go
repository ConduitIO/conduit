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

package conduit

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Current is the hardcoded version of Conduit. This value is updated by the
// `scripts/update-version.go` script during the release process.
// When building locally without `ldflags`, this is the default version.
const Current = "v0.1.0-develop" // This line will be modified by the automation script.

// version is set during the build process (i.e. the Makefile) via ldflags.
// It takes precedence over the Current constant.
var version string

// Version returns the current Conduit application version.
// It prioritizes the version injected via ldflags, then falls back to the
// `Current` constant, and finally tries `debug.ReadBuildInfo().Main.Version`.
// If appendOSArch is true, it appends the OS and architecture information.
func Version(appendOSArch bool) string {
	v := Current // Default to the hardcoded constant
	if version != "" {
		v = version // Override with ldflags if present
	} else if info, ok := debug.ReadBuildInfo(); ok {
		// Use module version from build info if it's not a development build
		// and current constant is still default.
		if info.Main.Version != "(devel)" && info.Main.Version != "" && v == Current {
			v = info.Main.Version
		}
	}

	if v == "" || v == "(devel)" { // Fallback if all else fails or Current is still initial/default
		v = "development"
	}

	if appendOSArch {
		v = fmt.Sprintf("%s %s/%s", v, runtime.GOOS, runtime.GOARCH)
	}
	return v
}
