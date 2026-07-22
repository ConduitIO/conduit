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

package registry

// This file exists ONLY to give the external registry_test package (in
// install_test.go) a seam into the unexported chaos-injection hook (see
// chaos.go) for the SIGKILL-simulation chaos tests. Being a _test.go file,
// none of this is compiled into a production build.

// SetChaosHookForTest installs a test-only crash-injection hook.
func SetChaosHookForTest(fn func(point string)) {
	chaosHook = fn
}

// Exported aliases of the unexported chaos point names, for the external
// test package to reference without duplicating the literal strings.
const (
	ChaosPointDownloadComplete      = chaosPointDownloadComplete
	ChaosPointExtractComplete       = chaosPointExtractComplete
	ChaosPointPrerenameFDOpened     = chaosPointPrerenameFDOpened
	ChaosPointPostRenamePreManifest = chaosPointPostRenamePreManifest
	ChaosPointIndexStateBeforeWrite = chaosPointIndexStateBeforeWrite
)
