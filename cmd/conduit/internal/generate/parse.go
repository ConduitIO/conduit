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

package generate

import (
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// extractCandidate pulls the pipeline-config YAML out of a raw model reply.
//
// Models wrap output in fences and narrate around it even when told not to,
// so this is a lenient reader by design: being strict here would spend a
// retry (a real API call) on a reply that already contained a perfectly good
// config. What it never does is REPAIR the YAML — anything it returns is the
// model's own text, passed to the same parser and validator a hand-written
// file goes through, so a lenient extraction cannot smuggle an invalid config
// past a gate.
//
// Precedence:
//  1. a fenced block containing a "version:" key (the config, even when the
//     reply also fences an unrelated snippet);
//  2. the first fenced block, whatever it holds;
//  3. unfenced text from the first "version:" line onward, dropping any
//     preamble prose above it.
//
// An empty reply, or one with no fence and no "version:" line, is an error:
// there is nothing here to validate, and inventing a candidate would be the
// one thing this package must never do.
func extractCandidate(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", cerrors.New("provider returned an empty response")
	}

	blocks := fencedBlocks(raw)
	for _, b := range blocks {
		if hasVersionKey(b) {
			return b, nil
		}
	}
	if len(blocks) > 0 {
		return blocks[0], nil
	}

	if idx := versionLineIndex(raw); idx >= 0 {
		return strings.TrimSpace(raw[idx:]) + "\n", nil
	}

	return "", cerrors.New("provider response contained no pipeline YAML (no fenced block and no version: key)")
}

// fencedBlocks returns the contents of every ``` fenced block in raw, in
// order, with the optional language tag stripped.
//
// An unterminated final fence is treated as running to the end of the reply:
// that is what a truncated response looks like, and handing the partial YAML
// to the validator produces a specific finding to feed back, where discarding
// it produces only "no YAML found".
func fencedBlocks(raw string) []string {
	const fence = "```"

	var out []string
	rest := raw
	for {
		start := strings.Index(rest, fence)
		if start < 0 {
			return out
		}
		rest = rest[start+len(fence):]

		// Strip the language tag on the opening fence's own line.
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			if tag := strings.TrimSpace(rest[:nl]); tag == "" || isLanguageTag(tag) {
				rest = rest[nl+1:]
			}
		}

		end := strings.Index(rest, fence)
		if end < 0 {
			if body := strings.TrimSpace(rest); body != "" {
				out = append(out, body+"\n")
			}
			return out
		}
		if body := strings.TrimSpace(rest[:end]); body != "" {
			out = append(out, body+"\n")
		}
		rest = rest[end+len(fence):]
	}
}

// isLanguageTag reports whether s is a bare fence language tag ("yaml",
// "yml") rather than the first line of the block's content.
func isLanguageTag(s string) bool {
	switch strings.ToLower(s) {
	case "yaml", "yml":
		return true
	default:
		return false
	}
}

// hasVersionKey reports whether s has a line starting a "version:" key at
// column zero — the pipeline config's own root key.
func hasVersionKey(s string) bool { return versionLineIndex(s) >= 0 }

// versionLineIndex returns the byte offset of the first line in s that starts
// (at column zero) with "version:", or -1. Column zero matters: a nested
// "version:" inside a connector's settings is not the document root.
func versionLineIndex(s string) int {
	const key = "version:"

	offset := 0
	for _, line := range strings.SplitAfter(s, "\n") {
		if strings.HasPrefix(line, key) {
			return offset
		}
		offset += len(line)
	}
	return -1
}
