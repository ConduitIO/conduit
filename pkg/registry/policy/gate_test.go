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

package policy_test

import (
	"fmt"
	"testing"

	"github.com/matryer/is"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/policy"
)

// TestDecide_FullContextMatrix is the actual security warranty for this
// gate (plan-v2 §6): every one of the 6 boolean Context fields' 64
// combinations, not just the "interesting" ones, is exercised and checked
// against a hand-derived expectation independent of Decide's own
// implementation.
func TestDecide_FullContextMatrix(t *testing.T) {
	for ttyN := 0; ttyN < 2; ttyN++ {
		for ciN := 0; ciN < 2; ciN++ {
			for mcpN := 0; mcpN < 2; mcpN++ {
				for opN := 0; opN < 2; opN++ {
					for envN := 0; envN < 2; envN++ {
						for confN := 0; confN < 2; confN++ {
							ctx := policy.Context{
								TTY:               ttyN == 1,
								CIEnv:             ciN == 1,
								IsMCP:             mcpN == 1,
								OperatorPolicy:    opN == 1,
								EnvVarSet:         envN == 1,
								TypedConfirmation: confN == 1,
							}
							name := fmt.Sprintf("TTY=%v/CI=%v/MCP=%v/OpPolicy=%v/EnvVar=%v/Confirmed=%v",
								ctx.TTY, ctx.CIEnv, ctx.IsMCP, ctx.OperatorPolicy, ctx.EnvVarSet, ctx.TypedConfirmation)
							t.Run(name, func(t *testing.T) {
								is := is.New(t)
								wantAllowed, wantCode := expected(ctx)

								gotAllowed, err := policy.Decide(ctx)
								is.Equal(gotAllowed, wantAllowed)

								if wantAllowed {
									is.NoErr(err)
									return
								}
								is.True(err != nil)
								ce, ok := conduiterr.Get(err)
								is.True(ok)
								is.Equal(ce.Code, wantCode)
							})
						}
					}
				}
			}
		}
	}
}

// expected is an independent (from gate.go) re-derivation of plan-v2 §6's
// behavioral matrix, so this test does not just re-assert Decide's own
// control flow back at itself.
func expected(ctx policy.Context) (allowed bool, code conduiterr.Code) {
	if !ctx.OperatorPolicy {
		return false, policy.CodeUnsignedInstallDisabledByPolicy
	}
	if ctx.IsMCP {
		return false, policy.CodeUnsignedInstallNonInteractive
	}
	nonInteractive := !ctx.TTY || ctx.CIEnv
	if nonInteractive {
		if ctx.EnvVarSet {
			return true, conduiterr.Code{}
		}
		return false, policy.CodeUnsignedInstallNonInteractive
	}
	// interactive
	if ctx.TypedConfirmation {
		return true, conduiterr.Code{}
	}
	return false, policy.CodeUnsignedInstallNonInteractive
}

// A handful of named, human-legible scenarios matching plan-v2 §6's table
// literally, as a readable cross-check on top of the exhaustive matrix
// above.
func TestDecide_NamedScenarios(t *testing.T) {
	tests := []struct {
		name        string
		ctx         policy.Context
		wantAllowed bool
		wantCode    conduiterr.Code
	}{
		{
			name:        "interactive TTY, typed confirmation given",
			ctx:         policy.Context{TTY: true, OperatorPolicy: true, TypedConfirmation: true},
			wantAllowed: true,
		},
		{
			name:        "interactive TTY, no confirmation yet",
			ctx:         policy.Context{TTY: true, OperatorPolicy: true},
			wantAllowed: false,
			wantCode:    policy.CodeUnsignedInstallNonInteractive,
		},
		{
			name:        "non-interactive, no env var",
			ctx:         policy.Context{OperatorPolicy: true},
			wantAllowed: false,
			wantCode:    policy.CodeUnsignedInstallNonInteractive,
		},
		{
			name:        "non-interactive, env var set",
			ctx:         policy.Context{OperatorPolicy: true, EnvVarSet: true},
			wantAllowed: true,
		},
		{
			name:        "CI=true even with a TTY forces non-interactive",
			ctx:         policy.Context{TTY: true, CIEnv: true, OperatorPolicy: true, EnvVarSet: true},
			wantAllowed: true,
		},
		{
			name:        "CI=true, TTY, no env var",
			ctx:         policy.Context{TTY: true, CIEnv: true, OperatorPolicy: true},
			wantAllowed: false,
			wantCode:    policy.CodeUnsignedInstallNonInteractive,
		},
		{
			name: "MCP-originated is always refused, even with everything else favorable",
			ctx: policy.Context{
				TTY: true, OperatorPolicy: true, EnvVarSet: true, TypedConfirmation: true, IsMCP: true,
			},
			wantAllowed: false,
			wantCode:    policy.CodeUnsignedInstallNonInteractive,
		},
		{
			name: "operator policy disables unconditionally, even with everything else favorable",
			ctx: policy.Context{
				TTY: true, EnvVarSet: true, TypedConfirmation: true, OperatorPolicy: false,
			},
			wantAllowed: false,
			wantCode:    policy.CodeUnsignedInstallDisabledByPolicy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			allowed, err := policy.Decide(tt.ctx)
			is.Equal(allowed, tt.wantAllowed)
			if tt.wantAllowed {
				is.NoErr(err)
				return
			}
			is.True(err != nil)
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, tt.wantCode)
		})
	}
}
