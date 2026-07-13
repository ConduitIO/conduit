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

package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/internal/llmsgen/allcodes"
)

// errorCodeInfo is the rendering-ready shape of one registered
// conduiterr.Code.
type errorCodeInfo struct {
	Reason      string
	GRPCCode    string
	Description string
}

// gatherErrorCodes returns every registered error code, sorted by reason
// (matching conduiterr.Codes()'s own contract), with a description sourced
// from that code's Register call-site doc comment where one was found by
// scanRegisteredCodes, or a deterministic fallback synthesized from the
// reason string otherwise.
//
// Completeness (design doc D5/AC-12): this reads through
// allcodes.Codes(), the blank-import barrel that brings every
// code-declaring package's init to life. gatherErrorCodes itself does not
// re-verify completeness — that is TestAllCodesComplete's job, comparing
// this same allcodes.Codes() set against an independent static scan of the
// whole repo for Register(...) call-sites, so a package that's missing
// from the barrel is caught by a failing test rather than a silently
// incomplete llms.txt.
func gatherErrorCodes(descriptions map[string]string) []errorCodeInfo {
	codes := allcodes.Codes()
	out := make([]errorCodeInfo, 0, len(codes))
	for _, c := range codes {
		desc, ok := descriptions[c.Reason()]
		if !ok || desc == "" {
			desc = fallbackDescription(c.Reason())
		}
		out = append(out, errorCodeInfo{
			Reason:      c.Reason(),
			GRPCCode:    c.GRPCCode().String(),
			Description: desc,
		})
	}
	// conduiterr.Codes() already sorts by reason; re-sort defensively per
	// D3 ("re-sort defensively" — the drift-guard's correctness must not
	// depend on an invariant owned by a different package staying true).
	sort.Slice(out, func(i, j int) bool { return out[i].Reason < out[j].Reason })
	return out
}

// fallbackDescription synthesizes a human-readable description from a
// dotted reason ("connector.plugin_not_found") when no doc comment was
// found at the code's Register call-site. It is pure string manipulation
// over the reason itself (source-derived, deterministic) — never a
// hand-maintained lookup table that could drift from the registry.
func fallbackDescription(reason string) string {
	domain, name, found := strings.Cut(reason, ".")
	if !found {
		return humanize(reason)
	}
	return fmt.Sprintf("%s: %s", humanize(domain), humanize(name))
}

// humanize turns a snake_case token into space-separated words.
func humanize(s string) string {
	return strings.ReplaceAll(s, "_", " ")
}
