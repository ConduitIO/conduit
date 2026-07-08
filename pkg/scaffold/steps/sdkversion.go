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

package steps

import "context"

// SetModuleVersion pins modulePath to version in dir's go.mod/go.sum via `go
// get`, for the --sdk-version override (Request.SDKVersion). This is
// network-dependent (it must resolve and download the requested version) —
// a failure here (a nonexistent version, no network) is treated the same as
// any other post-rewrite pipeline failure by scaffold.Generate: the staging
// directory is discarded, nothing partial is left at the destination.
func SetModuleVersion(ctx context.Context, dir, modulePath, version string) error {
	_, err := runGo(ctx, dir, "get", modulePath+"@"+version)
	return err
}
