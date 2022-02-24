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
var version string

func Version(appendOSArch bool) string {
	v := "development"
	if version != "" {
		v = version
	} else if info, ok := debug.ReadBuildInfo(); ok {
		v = info.Main.Version
	}

	if appendOSArch {
		v = fmt.Sprintf("%s %s/%s", v, runtime.GOOS, runtime.GOARCH)
	}
	return v
}
