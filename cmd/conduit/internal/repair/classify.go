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

package repair

import "strings"

// FixClass is the classifier's safety verdict on a ProposedFix, keyed on its
// conduiterr.Fix.ConfigPath. See classify's doc for the default-deny
// contract.
type FixClass string

const (
	// FixClassSafe is pipeline/processor metadata or a structural field
	// with no data-path effect — auto-appliable without escalation.
	FixClassSafe FixClass = "safe"
	// FixClassRestart would be an EffectRestart change
	// (pkg/provisioning/plan.go's Effect) if deployed to a running
	// pipeline, but is not itself ack/position/checkpoint-adjacent.
	// Auto-appliable to the file; flagged so the human/agent knows a
	// subsequent `deploy`/`apply` against a running pipeline would need to
	// stop it first.
	FixClassRestart FixClass = "restart"
	// FixClassDataPath is ack/position/checkpoint-adjacent config —
	// connector settings, a connector's own plugin/type, DLQ config, or any
	// *.id field. Refused for auto-apply; routed to the human Tier-1 path
	// (Escalate, CLI-only — see ApplyInput.Escalate's doc).
	FixClassDataPath FixClass = "data_path"
)

// classify classifies configPath (a conduiterr.Fix.ConfigPath — either
// pipeline-relative, e.g. "/status", or document-rooted, e.g.
// "/pipelines/0/processors/0/type"; classification is prefix-agnostic, see
// below) against the data-integrity invariants, before repair ever offers to
// apply it (design doc §4.2 — "the Tier-1 crux").
//
// The check order matters and is deliberately conservative / default-deny
// (mirrors deploy.NewLocalService's own default-deny posture):
//
//  1. An explicit DENY pattern (connector settings, a connector's own
//     plugin/type, DLQ config, any *.id field) always wins and classifies
//     FixClassDataPath, regardless of anything else about the path.
//  2. Only then is the path checked against the small, explicit v1 SAFE/
//     RESTART allowlists (design doc §6's four producer classes).
//  3. Anything that matches neither — an unrecognized ConfigPath — falls
//     through to FixClassDataPath. This is the default-deny guarantee
//     (AC-5): an omission in the deny-list is an invariant risk (a
//     data-path fix silently auto-applied); an omission in the allowlist
//     is merely "repair can't auto-apply this yet," the safe direction to
//     fail in.
//
// Matching is done on path SEGMENTS (split on "/", ignoring a leading
// empty segment from the leading "/"), not on a fixed prefix depth, so the
// same rules apply whether configPath is pipeline-relative (as
// pkg/provisioning/config.Validate's fixes are) or document-rooted (as the
// parser-warning rename fix is, see linter.go) — repair's own engine is the
// only caller that needs to reconcile the two addressing conventions (see
// yamlnode.go's documentPath), and it does so before storage, not before
// classification, so classify itself stays a pure, convention-agnostic
// pattern match.
func classify(configPath string) FixClass {
	segs := pathSegments(configPath)
	if len(segs) == 0 {
		return FixClassDataPath // default-deny: no path at all
	}

	// 1. Explicit deny patterns.
	for _, s := range segs {
		if s == "settings" || s == "dead-letter-queue" || s == "dlq" {
			return FixClassDataPath
		}
	}
	if segs[len(segs)-1] == "id" {
		return FixClassDataPath
	}
	// A connector's OWN plugin/type (".../connectors/<idx>/plugin" or
	// ".../connectors/<idx>/type") is data-path: changing which plugin a
	// connector runs, or whether it's a source/destination, is a
	// topology-changing, ack/position-adjacent edit. This is narrower than
	// "any field named plugin/type" — a PROCESSOR's plugin/type (v1 item
	// #1's rename target) is deliberately NOT caught here; see the safe
	// allowlist below.
	if n := len(segs); n >= 3 && segs[n-3] == "connectors" && (segs[n-1] == "plugin" || segs[n-1] == "type") {
		return FixClassDataPath
	}

	// 2. v1 SAFE / RESTART allowlists (design doc §6, items #1-#4).
	switch last := segs[len(segs)-1]; last {
	case "status", "description", "type":
		// "type" here is a processor's deprecated field (item #1's rename
		// target) or the pipeline-level /status — /connectors/*/type was
		// already classified data_path above and never reaches this
		// switch.
		return FixClassSafe
	case "workers":
		return FixClassRestart
	}

	// 3. Default-deny: an unrecognized ConfigPath is never safe.
	return FixClassDataPath
}

// pathSegments splits a JSON-pointer-shaped configPath into its non-empty
// segments — "/status" -> ["status"], "/connectors/0/settings/table" ->
// ["connectors","0","settings","table"]. An empty or root-only path yields
// an empty slice.
func pathSegments(configPath string) []string {
	trimmed := strings.Trim(configPath, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}
