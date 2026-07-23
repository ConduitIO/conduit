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

package policy

import "github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"

// Context is every input Decide's pure logic needs — no crypto, no I/O.
// The CLI (PR-2) is responsible for populating this honestly; Decide trusts
// whatever it is given.
type Context struct {
	// TTY reports whether the invocation is attached to an interactive
	// terminal (stdin/stdout).
	TTY bool
	// CIEnv reports whether a CI-style environment was detected (e.g.
	// CI=true), which forces the non-interactive path even if TTY somehow
	// reports true (a CI runner can attach a pty).
	CIEnv bool
	// IsMCP is hardcoded true by the MCP tool handler, NEVER derived from a
	// request field — the MCP repair_apply-style tool's JSON schema has no
	// allowUnsigned-shaped parameter at all, so there is nothing in an MCP
	// request that could set this. Decide refuses unconditionally whenever
	// this is true, regardless of every other field.
	IsMCP bool
	// OperatorPolicy is the operator's install.allowUnsigned config value.
	// false hard-disables the whole gate regardless of TTY/env var/MCP —
	// checked first, before any other field, so an operator's explicit
	// refusal can never be talked around by an interactive session.
	OperatorPolicy bool
	// EnvVarSet reports whether CONDUIT_ALLOW_UNSIGNED_INSTALL=I_UNDERSTAND
	// is set in the process environment — the non-interactive escape
	// hatch (CI/automation contexts that can't answer a TTY prompt).
	EnvVarSet bool
	// TypedConfirmation reports whether the caller already collected and
	// validated an exact-name interactive confirmation (the actual prompt
	// and string comparison are the CLI's job, PR-2; Decide only consumes
	// the already-validated result). Only consulted on the interactive
	// path (TTY && !CIEnv && !IsMCP).
	TypedConfirmation bool
}

// Decision is the opaque result of Decide (PR-2, plan-v2 §6/P1-3
// structural enforcement): the ONLY way code can prove "unsigned install is
// allowed" was actually decided BY Decide, never synthesized elsewhere.
// Decision's zero value reports Allowed() == false; the allowed field is
// unexported, so no package other than policy can construct a
// Decision{allowed: true} literal — this is a compiler-enforced guarantee,
// not a documentation convention, on top of the depguard rule in
// .golangci.yml restricting which files may even import this package.
type Decision struct {
	allowed bool
}

// Allowed reports whether Decide determined the unsigned-install path may
// proceed. The only way to obtain a Decision with Allowed() == true is a
// call to Decide that itself returned (Decision, nil) — see Decide's doc
// comment for exactly which Context shapes produce that outcome.
func (d Decision) Allowed() bool { return d.allowed }

// Decide is the ONLY function in this codebase permitted to return "skip
// verification" for connector installation (see package doc). It implements
// plan-v2 §6's behavioral matrix exactly:
//
//	Context                                              | Behavior
//	------------------------------------------------------------------------
//	OperatorPolicy == false                              | hard refuse, CodeUnsignedInstallDisabledByPolicy — checked first, wins over every other field
//	IsMCP == true                                         | hard refuse, CodeUnsignedInstallNonInteractive — MCP is never interactive and never gets its own escape hatch
//	non-interactive (!TTY || CIEnv), EnvVarSet == false   | refuse, CodeUnsignedInstallNonInteractive
//	non-interactive (!TTY || CIEnv), EnvVarSet == true    | allow, no prompt (an audit-log entry is the caller's job, PR-2)
//	interactive (TTY && !CIEnv && !IsMCP), confirmed      | allow
//	interactive (TTY && !CIEnv && !IsMCP), not confirmed  | refuse, CodeUnsignedInstallNonInteractive ("declined" — retry interactively, not a policy hard-stop)
//
// The two refusal codes are deliberately split (plan-v2 §12 decision item
// 4): CodeUnsignedInstallDisabledByPolicy means "an operator forbade this
// outright, no amount of retrying will change it"; every other refusal
// path here uses CodeUnsignedInstallNonInteractive, meaning "this
// particular attempt didn't clear the gate, but a different context
// (interactively, or with the env var) might".
// StaleBundleContext is every input DecideStaleBundle's pure logic needs —
// the offline-install analogue of Context, gated "exactly like
// --allow-unsigned" per DeVaris's ratification (plan-v2/step5 §7 step 3,
// §9 decision item 2): TTY/operator-policy gated, forbiddable, never
// silently flippable. A distinct type from Context (rather than reusing it)
// so the two gates' fields are never confused at a call site — in
// particular OperatorAllowStaleBundle is a different operator policy knob
// (install.allowStaleBundle) than OperatorPolicy's install.allowUnsigned.
type StaleBundleContext struct {
	// TTY, CIEnv, IsMCP, EnvVarSet, TypedConfirmation mirror Context's
	// fields exactly (see Context's doc comment) — the only difference is
	// which env var/config knob they represent (CONDUIT_ALLOW_STALE_BUNDLE
	// and install.allowStaleBundle, collected by the CLI layer, never by
	// this package).
	TTY               bool
	CIEnv             bool
	IsMCP             bool
	EnvVarSet         bool
	TypedConfirmation bool
	// OperatorAllowStaleBundle is the operator's install.allowStaleBundle
	// config value. false hard-disables the whole gate regardless of
	// flag/TTY/env var — checked first, exactly as Context.OperatorPolicy
	// is for the unsigned-install gate.
	OperatorAllowStaleBundle bool
}

// DecideStaleBundle is the offline-bundle-staleness analogue of Decide,
// implementing the identical behavioral shape (operator policy checked
// first and wins outright; MCP never gets an escape hatch; non-interactive
// requires the env var; interactive requires a typed confirmation) but
// returning a human-readable reason string instead of a registered error
// code: unlike Decide, this decision deliberately does NOT mint its own
// conduiterr codes for "why not allowed" — the single sanctioned caller
// (pkg/registry/bundle.go) already has the one canonical code this feature
// needs (registry.CodeBundleStale, plan-v2 §4) and attaches reason as that
// error's message, rather than this package inventing parallel codes
// step5/plan-v2 never asked for.
//
// Every allowed outcome is still routed through this function, and only
// this function ever produces Decision{allowed: true} for the stale-bundle
// path — the same structural guarantee Decide provides, via Decision's
// unexported field.
func DecideStaleBundle(ctx StaleBundleContext) (dec Decision, reason string) {
	if !ctx.OperatorAllowStaleBundle {
		return Decision{}, "stale-bundle installs are disabled by operator policy (install.allowStaleBundle: false)"
	}
	if ctx.IsMCP {
		return Decision{}, "stale-bundle installs are never permitted from the MCP tool"
	}

	nonInteractive := !ctx.TTY || ctx.CIEnv
	if nonInteractive {
		if ctx.EnvVarSet {
			return Decision{allowed: true}, ""
		}
		return Decision{}, "requires an interactive TTY confirmation, or CONDUIT_ALLOW_STALE_BUNDLE=I_UNDERSTAND set in a non-interactive context"
	}

	if ctx.TypedConfirmation {
		return Decision{allowed: true}, ""
	}
	return Decision{}, "stale bundle installation was not confirmed"
}

func Decide(ctx Context) (Decision, error) {
	if !ctx.OperatorPolicy {
		return Decision{}, conduiterr.New(CodeUnsignedInstallDisabledByPolicy,
			"unsigned connector installation is disabled by operator policy (install.allowUnsigned: false)")
	}

	if ctx.IsMCP {
		return Decision{}, conduiterr.New(CodeUnsignedInstallNonInteractive,
			"unsigned connector installation is never permitted from the MCP tool")
	}

	nonInteractive := !ctx.TTY || ctx.CIEnv
	if nonInteractive {
		if ctx.EnvVarSet {
			return Decision{allowed: true}, nil
		}
		return Decision{}, conduiterr.New(CodeUnsignedInstallNonInteractive,
			"unsigned connector installation requires an interactive TTY confirmation, or "+
				"CONDUIT_ALLOW_UNSIGNED_INSTALL=I_UNDERSTAND set in a non-interactive context")
	}

	// Interactive: TTY, not CI, not MCP.
	if ctx.TypedConfirmation {
		return Decision{allowed: true}, nil
	}
	return Decision{}, conduiterr.New(CodeUnsignedInstallNonInteractive,
		"unsigned connector installation was not confirmed")
}
