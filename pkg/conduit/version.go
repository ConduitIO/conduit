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

// appVersion is set during the build process via ldflags (i.e., the Makefile)
// It follows Go's convention for module version, where the version
// starts with the letter v, followed by a semantic version.
var appVersion string

// BuiltinConnectorVersion is the version that built-in connectors report.
// It is updated by a script (e.g., scripts/update-version.go) before compilation
// when making a release or setting a development version.
// The value here acts as a default for local development.
var BuiltinConnectorVersion = "v0.0.0-develop"

// Version returns the application version string. If appendOSArch is true,
// it appends the operating system and architecture information.
func Version(appendOSArch bool) string {
	v := "development"
	if appVersion != "" {
		v = appVersion
	} else if info, ok := debug.ReadBuildInfo(); ok {
		// Fallback to module version if ldflags didn't set appVersion
		v = info.Main.Version
	}

	if appendOSArch {
		v = fmt.Sprintf("%s %s/%s", v, runtime.GOOS, runtime.GOARCH)
	}
	return v
}

// GetBuiltinConnectorVersion returns the version string for built-in connectors.
// This version is updated programmatically by the CI/CD pipeline.
func GetBuiltinConnectorVersion() string {
	return BuiltinConnectorVersion
}
