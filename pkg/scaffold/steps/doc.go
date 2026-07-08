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

// Package steps implements the post-rewrite pipeline scaffold.Generate runs
// against a staged, renamed template tree: install the code-gen tool,
// generate, verify the result builds, and initialize git. Every function
// here operates on a plain directory (no embed.FS, no knowledge of the
// staging/atomic-rename discipline scaffold.Generate owns) so each step is
// independently testable against any on-disk tree.
//
// # InstallTool and Generate are not symmetric between connector and
// processor
//
// This is the load-bearing asymmetry the design doc's review flagged: the
// connector template's own `make generate` runs `conn-sdk-cli specgen`
// (regenerate connector.yaml from the Go config structs) then
// `conn-sdk-cli readmegen -w` (fill in the README's parameter tables); the
// processor template's `make generate` runs a single, different tool,
// `paramgen -output=paramgen_proc.go ProcessorConfig`. InstallTool and
// Generate each switch on template.Kind explicitly — there is no shared
// "install the tool" or "run generate" code path that happens to work for
// both by coincidence. See install.go and generate.go for exactly which
// commands run for which kind, verified against each template's actual
// setup.sh and Makefile at the pinned ref (see package template).
package steps
