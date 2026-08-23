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

// CacheMetaForTest exposes cacheMetaFor (cache.go) to the external
// registry_test package, so cache_test.go can assert on a populated
// entry's meta.json contents directly instead of re-parsing the file
// itself.
func CacheMetaForTest(connectorsPath, digestHex string) (CacheMeta, error) {
	return cacheMetaFor(connectorsPath, digestHex)
}

// CheckMinConduitVersionForTest exposes checkMinConduitVersion (resolve.go)
// — the minConduitVersion-specific comparison that carries the nightly-train
// prerelease carve-out — to the external registry_test package. Added for
// PR #2822's review Finding 1: the prior test suite only exercised this
// logic indirectly through Resolve/ResolveProcessor happy paths, which
// mutation testing showed left the carve-out's guard conditions
// unenforced (checkminversion_test.go tests this function directly against
// each of those mutations).
func CheckMinConduitVersionForTest(minVer, running string) error {
	return checkMinConduitVersion(minVer, running)
}

// CheckMinVersionForTest exposes the strict, no-carve-out checkMinVersion
// (resolve.go) — used to prove minProtocolVersion never receives the
// prerelease carve-out after PR #2822's review Finding 2 scope fix.
func CheckMinVersionForTest(label, minVer, running string) error {
	return checkMinVersion(label, minVer, running)
}
