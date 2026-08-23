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

package registry_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conduitio/conduit/pkg/registry"
	"github.com/conduitio/conduit/pkg/registry/index"
)

// TestCheckMinConduitVersion_MutationBoundaries exercises
// checkMinConduitVersion (resolve.go) directly, row by row, against every
// guard condition its doc comment promises. This test exists because PR
// #2822's review found the prior suite — Resolve/ResolveProcessor
// happy-path tests only — left all three of the carve-out's guard
// conditions (same patch, same minor, minV has no prerelease of its own)
// and the top-level Prerelease()!="" gate structurally unenforced: mutating
// any one of the four (dropping any single guard, or replacing the whole
// condition with "anything goes") left `go test ./pkg/registry/` green.
//
// Each row's name states which mutation it kills. Verified by hand during
// the #2822 review response: each of the four mutations from the review
// (drop the patch guard, drop the minor guard, drop minV's
// no-prerelease guard, and collapse the whole condition to
// "if runV.Prerelease() != \"\"") was applied to checkMinConduitVersion in
// turn and this test re-run; each left exactly the row(s) named after it
// failing (the anything-goes mutation additionally fails the two
// same-core-prerelease-ordering rows below, since it also disables
// ordinary rc.N-vs-rc.M precedence — expected, not a gap), then the guard
// was restored and a diff confirmed byte-identical to the pre-mutation
// file before moving on to the next row.
func TestCheckMinConduitVersion_MutationBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		minVer  string
		running string
		wantErr bool
	}{
		{
			// Kills: dropping `runV.Patch() == minV.Patch()`. Without that
			// guard, a same-(major,minor) prerelease with a LOWER patch
			// would satisfy a higher-patch minimum it genuinely doesn't
			// meet — running truly lacks 0.20.1's code.
			name:    "drop-patch-guard: 0.20.0-pre must not satisfy >=0.20.1",
			minVer:  "0.20.1",
			running: "0.20.0-pre",
			wantErr: true,
		},
		{
			// Kills: dropping `runV.Minor() == minV.Minor()`. Same idea one
			// level up — a same-major, lower-minor prerelease must not
			// satisfy a higher-minor minimum.
			name:    "drop-minor-guard: 0.20.0-pre must not satisfy >=0.21.0",
			minVer:  "0.21.0",
			running: "0.20.0-pre",
			wantErr: true,
		},
		{
			// Kills: dropping `minV.Prerelease() == ""`. The carve-out is
			// defined as "a prerelease of the exact RELEASE minimum
			// satisfies it" — it must not also let a lower prerelease
			// satisfy a HIGHER prerelease minimum at the same core.
			name:    "drop-minV-prerelease-empty-guard: 0.20.0-rc.0 must not satisfy >=0.20.0-rc.1",
			minVer:  "0.20.0-rc.1",
			running: "0.20.0-rc.0",
			wantErr: true,
		},
		{
			// Kills: replacing the whole `if runV.Prerelease() != "" && ...`
			// condition with just `if runV.Prerelease() != ""` (anything
			// goes for any prerelease running version, against any
			// minimum). Per the review, this mutation IS already caught by
			// TestResolve_PrereleaseDoesNotSatisfyHigherMinimum via
			// Resolve() — kept here too so the direct-function suite is a
			// complete mutation-kill set on its own, without relying on an
			// indirect caller's fixture to cover it.
			name:    "reject-anything-goes: a low-core prerelease must not satisfy a much higher minimum",
			minVer:  "99.0.0",
			running: "0.1.0-pre",
			wantErr: true,
		},
		// Positive controls: prove the guard rows above aren't just
		// vacuously true by also checking the carve-out still WORKS at its
		// intended boundary, and that ordinary same-core prerelease
		// precedence (untouched by the carve-out) behaves correctly in
		// both directions.
		{
			name:    "positive control: 0.20.0-pre satisfies its own exact release minimum",
			minVer:  "0.20.0",
			running: "0.20.0-pre",
			wantErr: false,
		},
		{
			name:    "ordinary same-core prerelease precedence: 0.20.0-rc.1 satisfies >=0.20.0-rc.0",
			minVer:  "0.20.0-rc.0",
			running: "0.20.0-rc.1",
			wantErr: false,
		},
		{
			name:    "ordinary same-core prerelease precedence: 0.20.0-rc.1 does not satisfy >=0.20.0-rc.2",
			minVer:  "0.20.0-rc.2",
			running: "0.20.0-rc.1",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := registry.CheckMinConduitVersionForTest(tt.minVer, tt.running)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestCheckMinVersion_NeverAppliesPrereleaseCarveOut proves checkMinVersion
// (the strict comparison, used directly for minProtocolVersion) has NO
// carve-out at all — the same exact-minimum prerelease case that
// checkMinConduitVersion accepts must be refused here.
func TestCheckMinVersion_NeverAppliesPrereleaseCarveOut(t *testing.T) {
	err := registry.CheckMinVersionForTest("minProtocolVersion", "0.20.0", "0.20.0-pre")
	require.Error(t, err)
}

// TestResolve_MinProtocolVersionNeverGetsPrereleaseCarveOut is PR #2822's
// review Finding 2, reproduced at the Resolve() level with the reviewer's
// own empirically-probed values: before the scope fix, checkMinVersion was
// SHARED between the minConduitVersion and minProtocolVersion call sites in
// checkCompatible, so the nightly-train prerelease carve-out silently
// widened the protocol gate too —
//
//	minProtocolVersion=0.10.0  runningProtocol=v0.10.0-rc.1  -> was ALLOWED (bug)
//	minProtocolVersion=0.10.1  runningProtocol=v0.10.0-rc.1  -> refused (unaffected boundary)
//
// even though the "nightly train" rationale for the carve-out — a build
// can install something gated to the release it is building toward — has
// no analogue for a wire protocol version. After the fix, the first case
// above must also refuse: minProtocolVersion goes through checkMinVersion
// (resolve.go), which never carries the carve-out.
func TestResolve_MinProtocolVersionNeverGetsPrereleaseCarveOut(t *testing.T) {
	payload := index.Payload{
		SchemaVersion: 1,
		Index:         index.IndexMeta{Version: 1, Timestamp: time.Now()},
		Connectors: []index.Connector{
			{
				Name: "probe",
				Publisher: index.Publisher{
					ExpectedOIDCIssuer:      "https://token.actions.githubusercontent.com",
					ExpectedIdentityPattern: "^https://github\\.com/example/probe/.*$",
				},
				Versions: []index.ConnectorVersion{
					{
						Version: "1.0.0", MinConduitVersion: "0.1.0", MinProtocolVersion: "0.10.0",
						Artifacts: []index.Artifact{{OS: "linux", Arch: "amd64", Kind: "standalone", URL: "http://example/probe.tar.gz", SHA256: "aa"}},
					},
				},
			},
		},
	}

	t.Run("exact-minimum prerelease protocol is refused, not silently widened", func(t *testing.T) {
		_, err := registry.Resolve(payload, registry.ResolveOptions{
			Name: "probe", RunningConduitVersion: "1.0.0", RunningProtocolVersion: "v0.10.0-rc.1",
		})
		requireCode(t, err, registry.CodeIncompatibleVersion)
	})

	t.Run("higher-minimum prerelease protocol is refused (unaffected boundary, sanity check)", func(t *testing.T) {
		higherMin := payload
		higherMin.Connectors = []index.Connector{payload.Connectors[0]}
		higherMin.Connectors[0].Versions = []index.ConnectorVersion{payload.Connectors[0].Versions[0]}
		higherMin.Connectors[0].Versions[0].MinProtocolVersion = "0.10.1"

		_, err := registry.Resolve(higherMin, registry.ResolveOptions{
			Name: "probe", RunningConduitVersion: "1.0.0", RunningProtocolVersion: "v0.10.0-rc.1",
		})
		requireCode(t, err, registry.CodeIncompatibleVersion)
	})
}
