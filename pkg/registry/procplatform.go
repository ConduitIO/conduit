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
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// WASMProcessorArtifactKind is the artifact kind for a standalone WASM
// processor build. It is arch-neutral: a single GOOS=wasip1 GOARCH=wasm module
// runs on every host wazero supports, so — unlike a connector's per-(os, arch)
// StandaloneArtifactKind builds — a processor version has exactly ONE artifact
// of this kind (design doc 20260727-registry-processor-artifacts, D1/D2).
const WASMProcessorArtifactKind = "wasm-processor"

// wasmProcessorOS and wasmProcessorArch are the fixed (GOOS, GOARCH) pair every
// standalone WASM processor artifact must declare. They are the wazero/WASI
// target, deliberately NOT the host's runtime.GOOS/runtime.GOARCH.
const (
	wasmProcessorOS   = "wasip1"
	wasmProcessorArch = "wasm"
)

// SelectProcessorArtifact returns the single arch-neutral WASM artifact of a
// processor version. It is the processor analogue of SelectArtifact, but it
// NEVER consults runtime.GOOS/runtime.GOARCH: a WASM processor is arch-neutral,
// so there is exactly one artifact and it is selected by a fixed
// (wasip1, wasm) rule, not by matching the host platform (design doc D2; ADR
// 20260727-processors-ride-connector-registry, fact 3).
//
// A processor artifact that is not Kind == WASMProcessorArtifactKind, or whose
// (OS, Arch) is not exactly (wasip1, wasm), is refused with
// CodeInvalidProcessorArtifact — a hard validation error, NOT a silent skip.
// This is the deliberate contrast with the connector path (design doc D2,
// failure mode 3): SelectArtifact skips an unrecognized artifact kind because a
// connector version legitimately carries many artifacts of different shapes and
// picks the host match; a processor version carries exactly one artifact, which
// must be the WASM one, so a host os/arch (or any other malformed shape) is a
// malformed or spoofed index entry that must be surfaced, never quietly
// ignored into "nothing to install". The single-Artifact field on
// ProcessorVersion means a per-host artifact list cannot even be expressed —
// this function closes the remaining "wrong single artifact" gap.
//
// The returned pointer aliases a copy of the version's Artifact; callers must
// not assume it points into v.
func SelectProcessorArtifact(procName string, v index.ProcessorVersion) (*index.Artifact, error) {
	a := v.Artifact
	if a.Kind != WASMProcessorArtifactKind {
		err := conduiterr.New(CodeInvalidProcessorArtifact, fmt.Sprintf(
			"processor %q version %s has artifact kind %q, but a processor artifact must be %q",
			procName, v.Version, a.Kind, WASMProcessorArtifactKind))
		err.Suggestion = "the registry index entry for this processor version is malformed; it should declare a single arch-neutral " + WASMProcessorArtifactKind + " artifact"
		return nil, err
	}
	if a.OS != wasmProcessorOS || a.Arch != wasmProcessorArch {
		err := conduiterr.New(CodeInvalidProcessorArtifact, fmt.Sprintf(
			"processor %q version %s declares os/arch %s/%s, but a WASM processor artifact must be %s/%s",
			procName, v.Version, a.OS, a.Arch, wasmProcessorOS, wasmProcessorArch))
		err.Suggestion = "a standalone WASM processor is arch-neutral and must be built for " + wasmProcessorOS + "/" + wasmProcessorArch + "; a host os/arch here is a malformed or spoofed index entry"
		return nil, err
	}
	return &a, nil
}
