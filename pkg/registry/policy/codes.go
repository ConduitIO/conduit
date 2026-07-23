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

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Policy error codes — the "policy"-owned rows of the canonical registry
// error table (plan-v2 §4), split per §12 decision item 4 so an agent/UI
// consumer can distinguish "retry interactively or set the env var" from
// "an operator forbade this outright" without string-matching Suggestion
// text.
var (
	// CodeUnsignedInstallNonInteractive is raised when --allow-unsigned was
	// requested in a non-interactive context (no TTY, CI=true, or
	// MCP-originated) without the escape-hatch env var also set, or when an
	// interactive typed confirmation was not given.
	CodeUnsignedInstallNonInteractive = conduiterr.Register("registry.unsigned_install_non_interactive", codes.PermissionDenied)
	// CodeUnsignedInstallDisabledByPolicy is raised when operator policy
	// (install.allowUnsigned: false) hard-disables the gate regardless of
	// flag/TTY/env var.
	CodeUnsignedInstallDisabledByPolicy = conduiterr.Register("registry.unsigned_install_disabled_by_policy", codes.PermissionDenied)
)
