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

package connectors

import "github.com/conduitio/conduit/pkg/registry/index"

// SetDefaultTrustAnchorsForTest overrides defaultTrustAnchors (the compiled-
// in Conduit registry root/freshness keys — empty in this build, pending the
// bootstrap ceremony, plan-v2 §9) for install_test.go's happy-path CLI
// tests, which need to verify against a locally-generated, test-only
// signing key rather than production anchors that don't exist yet. Restores
// the previous value via the returned func, following the same pattern as
// pkg/registry/export_test.go's SetChaosHookForTest. Being a _test.go file,
// none of this compiles into a production build.
func SetDefaultTrustAnchorsForTest(anchors index.TrustAnchors) (restore func()) {
	prev := defaultTrustAnchors
	defaultTrustAnchors = anchors
	return func() { defaultTrustAnchors = prev }
}

// UnsignedInstallEnvVarForTest exposes unsignedInstallEnvVar (the
// --allow-unsigned non-interactive escape-hatch env var name) to
// install_test.go, so the CLI-level operator-policy test doesn't hardcode a
// second copy of the literal that could silently drift from the real
// constant.
const UnsignedInstallEnvVarForTest = unsignedInstallEnvVar
