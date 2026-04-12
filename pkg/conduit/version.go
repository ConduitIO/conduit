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

// version is set during the build process (i.e. the Makefile)
// It follows Go's convention for module version, where the version
// starts with the letter v, followed by a semantic version.
// This value is updated by the CI/CD pipeline during releases.
var version = "development" // Initialize with a default string literal for automation

func Version(appendOSArch bool) string {
	v := version // Start with the value from the 'version' variable (can be updated by ldflags or script)
	if v == "" || v == "development" { // If version is empty (e.g., initial state) or still "development" (not overwritten by ldflags)
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "(devel)" && info.Main.Version != "" {
			v = info.Main.Version // Fallback to module version if available and not "(devel)"
		} else {
			v = "development" // Final fallback if no other version info is useful
		}
	}

	if appendOSArch {
		v = fmt.Sprintf("%s %s/%s", v, runtime.GOOS, runtime.GOARCH)
	}
	return v
}
