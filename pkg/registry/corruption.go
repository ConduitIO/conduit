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
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// CheckCorruption compares the digest computed from the ACTUALLY RECEIVED
// bytes (got, from Download's incremental hash) against the index's
// declared sha256 (want, an optionally "sha256:"-prefixed 64-character hex
// string).
//
// This is integrity, not trust: a byte-for-byte match here detects
// transport-level corruption (a bit-flip, a truncated transfer, a
// compromised CDN edge serving different bytes) but says nothing about WHO
// produced those bytes — proving that is ArtifactVerifier.VerifyArtifact's
// job (the actual authorization/trust boundary), run separately and always
// AFTER this check, never in place of it.
func CheckCorruption(got [32]byte, want string) error {
	want = strings.TrimPrefix(want, "sha256:")
	wantBytes, err := hex.DecodeString(want)
	if err != nil || len(wantBytes) != len(got) {
		return conduiterr.New(CodeCorruptDownload, fmt.Sprintf(
			"index sha256 %q is not a valid 64-character hex digest", want))
	}
	for i := range got {
		if got[i] != wantBytes[i] {
			return conduiterr.New(CodeCorruptDownload, fmt.Sprintf(
				"downloaded artifact's sha256 (%x) does not match the index's declared sha256 (%s) — "+
					"the download may have been corrupted or tampered with in transit; retrying is a fresh "+
					"download attempt, not a recovery of the same lost bytes", got, want))
		}
	}
	return nil
}
