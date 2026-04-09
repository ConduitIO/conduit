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

// version is set by the `make update-version` target or defaults to "v0.0.0-dev".
// It follows Go's convention for module version, where the version
// starts with the letter v, followed by a semantic version.
var version = "v0.0.0-dev"

func Version(appendOSArch bool) string {
	v := version // Start with the explicit constant from the source file

	// If the constant is still the default development version,
	// try to get a more specific version from build info.
	// This covers cases where `make update-version` wasn't run (e.g., local dev build),
	// but `go build` was, which might populate debug.ReadBuildInfo().Main.Version.
	if v == "v0.0.0-dev" {
		if info, ok := debug.ReadBuildInfo(); ok {
			// debug.ReadBuildInfo().Main.Version gives the Go module version.
			// If built from a tagged commit, it's the tag (e.g., "v1.2.3").
			// Otherwise, it might be "(devel)" or "v0.0.0-unofficial".
			// We only want to use it if it's a "real" version.
			if info.Main.Version != "" &&
				info.Main.Version != "(devel)" &&
				info.Main.Version != "v0.0.0-unofficial" {
				v = info.Main.Version
			}
		}
	}

	// Final fallback if nothing else yielded a proper version
	if v == "v0.0.0-dev" {
		v = "development"
	}

	if appendOSArch {
		v = fmt.Sprintf("%s %s/%s", v, runtime.GOOS, runtime.GOARCH)
	}
	return v
}
