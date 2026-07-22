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

package index

import json "github.com/goccy/go-json"

// Signature is one detached signature over the JCS-canonicalized bytes of
// the sibling payload (R-1 §a). role distinguishes the reviewer-gated root
// key (authorizes content) from the unattended freshness key (may only
// extend index.timestamp/bump index.version over byte-identical
// connectors[]) — see freeze.go and R-1's OQ3 resolution.
type Signature struct {
	Role      string `json:"role"`
	KeyID     string `json:"keyId"`
	Algorithm string `json:"algorithm"`
	Signature string `json:"signature"`
}

// rawEnvelope is the outer, generic parse of the served document: payload
// is kept as raw bytes so it can be (a) duplicate-key-checked and (b) JCS
// canonicalized and signature-verified BEFORE any schema-version-specific
// interpretation — R-1 §a's load-bearing ordering requirement. It is
// unexported: callers go through ParseUnverified (and, from PR-2, Verify),
// never this struct directly, so the "don't trust this yet" framing can't
// leak past this file.
type rawEnvelope struct {
	Payload    json.RawMessage `json:"payload"`
	Signatures []Signature     `json:"signatures"`
}
