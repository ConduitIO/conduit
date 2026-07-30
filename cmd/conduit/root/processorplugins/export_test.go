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

package processorplugins

import "github.com/conduitio/conduit/pkg/registry/index"

// SetDefaultTrustAnchorsForTest overrides this package's snapshot of the
// compiled-in registry anchors for install_test.go's happy-path CLI tests,
// which verify against a locally-generated, test-only signing key rather than
// the embedded production anchors. Restores the previous value via the returned
// func. Mirrors connectors.SetDefaultTrustAnchorsForTest — each install test
// binary overrides its own package's copy. Being a _test.go file, none of this
// compiles into a production build.
func SetDefaultTrustAnchorsForTest(anchors index.TrustAnchors) (restore func()) {
	prev := defaultTrustAnchors
	defaultTrustAnchors = anchors
	return func() { defaultTrustAnchors = prev }
}

// SetAnchorLoadErrForTest forces the "embedded anchors failed to load" state (a
// broken/anchor-stripped build) so the CLI's trust_anchors_unavailable refusal
// can be exercised end to end without corrupting the embed. Restores the
// previous value via the returned func.
func SetAnchorLoadErrForTest(err error) (restore func()) {
	prev := errAnchorLoad
	errAnchorLoad = err
	return func() { errAnchorLoad = prev }
}

// UnsignedInstallEnvVarForTest exposes the --allow-unsigned non-interactive
// escape-hatch env var name to install_test.go so it doesn't hardcode a second
// copy of the literal that could drift from the real constant.
const UnsignedInstallEnvVarForTest = "CONDUIT_ALLOW_UNSIGNED_INSTALL"
