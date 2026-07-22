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

import (
	"bytes"
	"fmt"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// maxNestingDepth bounds checkObject/checkArray's recursion (P0-2, plan-v2
// §2.4 item 2) so an attacker-crafted index with thousands of nested empty
// objects/arrays cannot exhaust the goroutine stack before any signature is
// even checked — refuse, never crash. The frozen schema's actual shape
// nests at most 4–5 levels deep (payload → connectors[] → versions[] →
// artifacts[] → signature), so 64 is generous headroom, not a tight fit
// tuned to the current shape.
const maxNestingDepth = 64

// CheckNoDuplicateKeys walks raw as generic JSON and refuses any object
// containing a duplicate key at any nesting level, before and independent
// of JCS canonicalization. This is required, not a style preference: JCS
// fixes serialization ambiguity, not parse-time duplicate-key resolution —
// a last-key-wins parse is a producer/verifier differential that JCS alone
// does not close, and per R-1 §a is itself "a signature-bypass primitive"
// (two parsers could legitimately disagree on which value a duplicated key
// held, while both verify the same signature over the same bytes).
//
// It is deliberately narrow: a structural walk only, checked before
// ParseUnverified attempts to interpret schemaVersion or unmarshal the
// typed schema — it does not itself validate that raw otherwise matches the
// schema (a duplicate-free document can still fail ParseUnverified's later
// typed unmarshal).
//
// A duplicate key found at any depth reports CodeIndexIntegrity (see that
// code's doc comment for why duplicate-key rejection is filed under the
// same code as signature-verification failure, not a separate one); nesting
// deeper than maxNestingDepth reports the distinct CodeIndexNestingTooDeep,
// so an operator/agent can tell "this index is too deeply nested to be
// legitimate" from "this index's content was tampered with".
//
// CheckNoDuplicateKeys recovers from a panic in the underlying decoder and
// reports it as CodeIndexIntegrity rather than crashing the process. This
// is not defensive-programming theater: the P0-2 fuzz corpus
// (FuzzDuplicateKeyWalk) found goccy/go-json's streaming Decoder.Token()
// panics (an internal slice index out of range), not merely errors, on
// certain malformed multi-byte-adjacent-invalid-UTF-8 input — exactly the
// "attacker-controlled bytes parsed before any crypto check" scenario P0-2
// exists to harden. The failing seed is preserved under
// testdata/fuzz/FuzzDuplicateKeyWalk/ as a permanent regression case.
func CheckNoDuplicateKeys(raw []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = conduiterr.New(CodeIndexIntegrity,
				fmt.Sprintf("index JSON could not be parsed safely: %v", r))
		}
	}()
	dec := json.NewDecoder(bytes.NewReader(raw))
	return checkValue(dec, 0)
}

// checkValue consumes exactly one JSON value (scalar, object, or array)
// from dec and recurses into it if it is an object or array.
func checkValue(dec *json.Decoder, depth int) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // scalar: string, float64, bool, nil — nothing to walk.
	}
	switch delim {
	case json.Delim('{'):
		return checkObject(dec, depth)
	case json.Delim('['):
		return checkArray(dec, depth)
	default:
		// '}' or ']' cannot legally appear as the *first* token of a value;
		// the decoder enforces well-formedness ahead of us. Never panic
		// regardless — treat as nothing further to walk.
		return nil
	}
}

func checkObject(dec *json.Decoder, depth int) error {
	if depth >= maxNestingDepth {
		return conduiterr.New(CodeIndexNestingTooDeep,
			"index JSON nests deeper than this build allows")
	}
	seen := make(map[string]struct{})
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			// An object key that isn't a string is malformed JSON; the
			// decoder should already refuse this, but never trust that
			// blindly — fail closed rather than index out of bounds later.
			return conduiterr.New(CodeIndexIntegrity, "index JSON object key is not a string")
		}
		if _, dup := seen[key]; dup {
			return conduiterr.New(CodeIndexIntegrity, "index JSON contains a duplicate key: "+key)
		}
		seen[key] = struct{}{}
		if err := checkValue(dec, depth+1); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil { // consume the closing '}'
		return err
	}
	return nil
}

func checkArray(dec *json.Decoder, depth int) error {
	if depth >= maxNestingDepth {
		return conduiterr.New(CodeIndexNestingTooDeep,
			"index JSON nests deeper than this build allows")
	}
	for dec.More() {
		if err := checkValue(dec, depth+1); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil { // consume the closing ']'
		return err
	}
	return nil
}
