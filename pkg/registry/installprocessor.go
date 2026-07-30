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

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	proc_standalone "github.com/conduitio/conduit/pkg/plugin/processor/standalone"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// develGuestVersion is the version a standalone WASM processor reports from Go
// build info when it was built with a plain `GOOS=wasip1 GOARCH=wasm go build`
// (no release version stamped in) — Go's module-version default for anything
// not built via `go install module@version`. The authoritative version for
// such a build is the registry index's name@version (what lands on disk and in
// the manifest), so install-time validation asserts the guest NAME but treats
// a "(devel)" guest VERSION as an unversioned dev build and does NOT require it
// to equal the index version. See validateProcessorWASM.
const develGuestVersion = "(devel)"

// wasmProcessorFileMode is 0644, not the connector path's 0755: a .wasm is read
// by wazero, never exec'd, so the executable bit is meaningless (plan edge
// case 14).
const wasmProcessorFileMode os.FileMode = 0o644

// ProcessorFileName builds the on-disk filename for an installed standalone
// WASM processor: conduit-processor-<name>_<version>.wasm. The name is cosmetic
// to discovery (the standalone registry registers a plugin under the module's
// own Specification().Name, not the filename) but gives uninstall/audit a
// predictable target and makes --processors.path human-legible; the .wasm
// suffix tells an operator eyeballing the directory what the file is (plan edge
// case 13). Exported for the PR-C CLI/uninstall, which must agree with this
// pipeline's naming without duplicating it.
func ProcessorFileName(name, version string) string {
	return fmt.Sprintf("conduit-processor-%s_%s.wasm", name, version)
}

// InstallProcessor is the processor analogue of Install. It fetches +
// shape-checks the signed index, resolves name/version against
// payload.Processors (ResolveProcessor), selects the single arch-neutral
// (wasip1/wasm) artifact (SelectProcessorArtifact — NEVER the host-platform
// SelectArtifact), and runs the SAME download → corruption-check → trust gate →
// atomic-rename → manifest → audit core Install uses (installArtifact). It
// differs from Install only in:
//
//   - the target directory: --processors.path (opts.ProcessorsPath), with its
//     own separate <processors.path>/.registry bookkeeping;
//   - the on-disk filename (conduit-processor-<name>_<version>.wasm) and mode
//     (0644);
//   - the manifest Kind (WASMProcessorArtifactKind) and audit label
//     ("processor_install"); and
//   - an install-time WASM compile+spec validation step (validateProcessorWASM)
//     run on the extracted artifact BEFORE the atomic rename.
//
// The trust core is reused verbatim, never forked (ADR decision 1): the same
// IndexVerifier/ArtifactVerifier, the same RequireProvenance behavior, and the
// same single policy.Decide unsigned gate (guarded by the PolicyBypass
// depguard rule). Integrity (sha256) stays separate from trust, as for
// connectors.
func InstallProcessor(ctx context.Context, opts InstallOptions) (*InstallResult, error) {
	if err := opts.validateProcessor(); err != nil {
		return nil, err
	}

	// 1-3: fetch + shape-check the index, resolve name/version over the
	// processor collection, select the single arch-neutral WASM artifact.
	raw, err := fetchIndexRaw(ctx, opts)
	if err != nil {
		return nil, err
	}
	verified, err := opts.IndexVerifier.VerifyIndex(ctx, raw)
	if err != nil {
		return nil, err
	}

	resolved, err := ResolveProcessor(verified.Payload, ResolveOptions{
		Name:                   opts.Name,
		Version:                opts.Version,
		RunningConduitVersion:  opts.RunningConduitVersion,
		RunningProtocolVersion: opts.RunningProtocolVersion,
	})
	if err != nil {
		return nil, err
	}

	artifact, err := SelectProcessorArtifact(resolved.Processor.Name, resolved.Version)
	if err != nil {
		return nil, err
	}

	ra := resolvedArtifact{
		name:    resolved.Processor.Name,
		version: resolved.Version.Version,
		identity: trust.PinnedIdentity{
			OIDCIssuer:      resolved.Processor.Publisher.ExpectedOIDCIssuer,
			IdentityPattern: resolved.Processor.Publisher.ExpectedIdentityPattern,
		},
		artifact:          artifact,
		versionProvenance: resolved.Version.SLSAProvenance,
		deprecated:        resolved.Version.Deprecated,
	}

	if opts.DryRun {
		return &InstallResult{
			Name: ra.name, Version: ra.version,
			OS: artifact.OS, Arch: artifact.Arch,
			Deprecated:  ra.deprecated,
			DryRun:      true,
			ArtifactURL: artifact.URL,
			Size:        artifact.Size,
		}, nil
	}

	// 4-9: lock, download, verify, extract, WASM-validate, atomically install,
	// record — into ProcessorsPath, with the processor target.
	return installArtifact(ctx, opts, opts.ProcessorsPath, wasmProcessorTarget(), verified, ra)
}

// wasmProcessorTarget is the installTarget for a standalone WASM processor: the
// conduit-processor-<name>_<version>.wasm filename, 0644 mode,
// WASMProcessorArtifactKind manifest Kind, the "processor_install" audit label,
// and validateProcessorWASM as the install-time validation hook.
func wasmProcessorTarget() installTarget {
	return installTarget{
		kind:       WASMProcessorArtifactKind,
		finalName:  ProcessorFileName,
		fileMode:   wasmProcessorFileMode,
		auditEvent: "processor_install",
		validate:   validateProcessorWASM,
	}
}

// validateProcessorWASM is the install-time validation gate (ADR decision 4 /
// plan AC-9). It compiles the extracted .wasm with the SAME wazero runtime the
// standalone processor registry uses at load time and extracts its
// Specification — refusing, BEFORE the artifact is renamed into place:
//
//   - a module that will not compile or whose spec cannot be read; and
//   - a module whose self-declared Specification().Name disagrees with the
//     resolved index name (never install a file that would register under a
//     different name than requested — closing the gap between the index name
//     and the name the runtime actually keys discovery on).
//
// Version handling honors the "(devel)" dev-build sentinel: a plain
// `GOOS=wasip1 GOARCH=wasm go build` stamps the module version as "(devel)",
// and the authoritative version is the index's, so a "(devel)" (or empty)
// guest version is accepted without an equality check; any OTHER concrete guest
// version that disagrees with the index is refused as a mispublished artifact.
//
// It runs through proc_standalone.InspectSpecification — the one, shared WASM
// loader — so install-time validation can never diverge from runtime
// discovery. There is no import cycle: the standalone package does not import
// pkg/registry (verified with `go list -deps`). A no-op logger is used; the
// step is silent unless it refuses.
//
// All refusals use CodeInvalidProcessorArtifact (FailedPrecondition): the
// fetched artifact, as published, does not meet the precondition for install.
func validateProcessorWASM(ctx context.Context, wasmPath, name, version string) error {
	spec, err := proc_standalone.InspectSpecification(ctx, log.Nop(), wasmPath)
	if err != nil {
		e := conduiterr.Wrap(CodeInvalidProcessorArtifact, fmt.Sprintf(
			"processor artifact for %q@%s failed install-time WASM validation: could not compile the module or read its specification", name, version), err)
		e.Suggestion = "the downloaded artifact is not a loadable standalone WASM processor; the registry index entry may point at a corrupt or wrong file"
		return e
	}

	if spec.Name != name {
		e := conduiterr.New(CodeInvalidProcessorArtifact, fmt.Sprintf(
			"processor artifact declares name %q but the registry index resolved it as %q — refusing to install a file that would register under a different name than requested",
			spec.Name, name))
		e.Suggestion = "the index entry name and the WASM module's own Specification().Name must match; this is a mispublished or spoofed index entry"
		return e
	}

	if spec.Version != develGuestVersion && spec.Version != "" && !processorVersionsAgree(spec.Version, version) {
		e := conduiterr.New(CodeInvalidProcessorArtifact, fmt.Sprintf(
			"processor artifact declares version %q but the registry index resolved version %q", spec.Version, version))
		e.Suggestion = `the index entry version and the WASM module's own Specification().Version must match (a plain dev build reporting "(devel)" is exempt)`
		return e
	}

	return nil
}

// processorVersionsAgree reports whether a guest-declared spec version matches
// the resolved index version. It compares via NormalizeVersion (so "v1.2.0"
// and "1.2.0" agree, matching the rest of the pipeline's version handling), and
// falls back to exact string equality when either side does not parse as
// semver.
func processorVersionsAgree(guest, indexVersion string) bool {
	gv, gerr := NormalizeVersion(guest)
	iv, ierr := NormalizeVersion(indexVersion)
	if gerr == nil && ierr == nil {
		return gv.Equal(iv)
	}
	return guest == indexVersion
}

// validateProcessor is InstallProcessor's InstallOptions check — validate()'s
// processor twin: it requires ProcessorsPath (not ConnectorsPath) and, like
// validate(), fails closed on a nil verifier rather than nil-panicking deeper
// in the pipeline.
func (o *InstallOptions) validateProcessor() error {
	if o.Name == "" {
		return conduiterr.New(conduiterr.CodeInvalidArgument, "processor name is required")
	}
	if o.ProcessorsPath == "" {
		return conduiterr.New(conduiterr.CodeInvalidArgument, "--processors.path is required")
	}
	if o.IndexURL == "" && o.IndexFile == "" {
		return conduiterr.New(conduiterr.CodeInvalidArgument, "one of --index-url or --index-file is required")
	}
	if o.IndexVerifier == nil || o.ArtifactVerifier == nil {
		return conduiterr.New(CodeVerificationUnavailable, "no verifier configured for this install")
	}
	return nil
}
