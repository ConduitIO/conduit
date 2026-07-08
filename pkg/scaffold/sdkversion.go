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

package scaffold

import (
	"context"

	"github.com/conduitio/conduit/pkg/scaffold/steps"
)

// sdkModule returns the Go module path of kind's SDK, for --sdk-version.
func sdkModule(kind Kind) string {
	if kind == KindProcessor {
		return "github.com/conduitio/conduit-processor-sdk"
	}
	return "github.com/conduitio/conduit-connector-sdk"
}

// applySDKVersion overrides the SDK version pinned in the embedded
// snapshot, via `go get` in the staged directory. Called after Rewrite (so
// go.mod's module line is already the caller's, not the placeholder) and
// before InstallTool/Generate/Build.
func applySDKVersion(ctx context.Context, dir string, kind Kind, version string) error {
	return steps.SetModuleVersion(ctx, dir, sdkModule(kind), version)
}
