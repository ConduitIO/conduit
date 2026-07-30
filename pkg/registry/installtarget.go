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

import (
	"context"
	"fmt"
	"os"

	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// resolvedArtifact is the artifact-kind-neutral bundle the shared install core
// (installArtifact → downloadVerifyAndInstall → runVerificationGate →
// finalizeArtifactInstall) consumes. Both the connector path (Install) and the
// processor path (InstallProcessor) populate it from their own resolved index
// entry, so the trust core — signature/provenance verification, the pinned
// identity check, and the policy.Decide unsigned gate — is exercised through
// ONE code path and never forked (ADR 20260727-processors-ride-connector-
// registry, decision 1).
type resolvedArtifact struct {
	// name/version are the resolved index name@version — the authoritative
	// on-disk/manifest identity (for a processor this is what the file is
	// named and keyed as, even when the WASM's own Specification().Version is
	// a "(devel)" dev-build sentinel; see installprocessor.go).
	name    string
	version string

	// identity is the publisher identity pin the artifact's signature +
	// provenance are verified against.
	identity trust.PinnedIdentity

	// artifact is the single selected artifact: for a connector the host
	// (os, arch) build chosen by SelectArtifact; for a processor the single
	// arch-neutral (wasip1/wasm) build chosen by SelectProcessorArtifact.
	artifact *index.Artifact

	// versionProvenance is the version-level SLSA provenance used as a
	// fallback when the artifact carries none of its own (fetchArtifactRef
	// prefers artifact.SLSAProvenance).
	versionProvenance *index.ProvenanceRef

	// deprecated mirrors the resolved version's Deprecated flag —
	// informational only, surfaced in the result; never a refusal.
	deprecated bool
}

// installTarget parameterizes the artifact-kind-specific tail of the shared
// install pipeline. connectorTarget reproduces the pre-existing connector
// behavior exactly (the unchanged connector test suite is the regression
// guard); wasmProcessorTarget (installprocessor.go) supplies the processor
// specifics. Nothing outside these two constructors ever builds an
// installTarget.
type installTarget struct {
	// kind is written to ManifestEntry.Kind for a completed install.
	kind string
	// finalName builds the on-disk filename (no directory) for the installed
	// artifact.
	finalName func(name, version string) string
	// fileMode is the chmod applied to the installed artifact.
	fileMode os.FileMode
	// auditEvent is the AuditEvent.Event label.
	auditEvent string
	// validate, if non-nil, runs on the extracted artifact (still inside the
	// private staging directory) AFTER the corruption + trust gates and BEFORE
	// the atomic rename. It is the seam for install-time WASM compile+spec
	// validation; nil for connectors, so their path is unchanged.
	validate func(ctx context.Context, stagedPath, name, version string) error
}

// connectorTarget is the default install target: it reproduces today's
// connector behavior byte-for-byte — the conduit-connector-<name>_<version>
// filename, 0755 mode, StandaloneArtifactKind manifest Kind, the
// "connector_install" audit label, and no install-time validation hook.
func connectorTarget() installTarget {
	return installTarget{
		kind:       StandaloneArtifactKind,
		finalName:  func(name, version string) string { return fmt.Sprintf("conduit-connector-%s_%s", name, version) },
		fileMode:   0o755,
		auditEvent: "connector_install",
		validate:   nil,
	}
}
