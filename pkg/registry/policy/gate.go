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
func Decide(ctx Context) (allowed bool, err error) {
	if !ctx.OperatorPolicy {
		return false, conduiterr.New(CodeUnsignedInstallDisabledByPolicy,
			"unsigned connector installation is disabled by operator policy (install.allowUnsigned: false)")
	}

	if ctx.IsMCP {
		return false, conduiterr.New(CodeUnsignedInstallNonInteractive,
			"unsigned connector installation is never permitted from the MCP tool")
	}

	nonInteractive := !ctx.TTY || ctx.CIEnv
	if nonInteractive {
		if ctx.EnvVarSet {
			return true, nil
		}
		return false, conduiterr.New(CodeUnsignedInstallNonInteractive,
			"unsigned connector installation requires an interactive TTY confirmation, or "+
				"CONDUIT_ALLOW_UNSIGNED_INSTALL=I_UNDERSTAND set in a non-interactive context")
	}

	// Interactive: TTY, not CI, not MCP.
	if ctx.TypedConfirmation {
		return true, nil
	}
	return false, conduiterr.New(CodeUnsignedInstallNonInteractive,
		"unsigned connector installation was not confirmed")
}
