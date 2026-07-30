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

import (
	"github.com/conduitio/conduit/cmd/conduit/root/connectors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// defaultTrustAnchors and errAnchorLoad snapshot the connectors package's
// compiled-in registry root/freshness anchors. There is ONE embedded anchor
// set for the whole CLI — the ceremony writes its PEMs into the connectors
// package's trustanchors/ directory, and both `conduit connectors install` and
// `conduit processor-plugins install` verify against it. This package does NOT
// embed a second copy (that would fork a security-critical asset); it consumes
// the same loaded set via connectors' exported accessors. The snapshot is safe:
// Go runs the connectors package's init (which populates the anchors) before
// this package's var initializers, because this package imports connectors.
//
// Tests override defaultTrustAnchors via SetDefaultTrustAnchorsForTest
// (export_test.go), exactly as connectors' own install tests override theirs —
// each test binary overrides its own package's copy.
var (
	defaultTrustAnchors index.TrustAnchors = connectors.DefaultTrustAnchors()
	errAnchorLoad                          = connectors.AnchorLoadErr()
)

// guardTrustAnchors mirrors connectors.guardTrustAnchors: on a build whose
// embedded anchors failed to load, refuse up front with the distinct,
// machine-actionable registry.trust_anchors_unavailable ("reinstall conduit")
// rather than a generic expired-anchor error. A load failure can only block an
// install, never let an unverified one through.
func guardTrustAnchors() error {
	if errAnchorLoad != nil {
		return conduiterr.Wrap(registry.CodeTrustAnchorsUnavailable,
			"this conduit build has no usable registry trust anchors (a build/release defect — reinstall a release build of conduit); processor installation cannot verify indexes without them",
			errAnchorLoad)
	}
	return nil
}
