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

// Package scaffoldcmd implements the shared command body behind `conduit
// connector new` and `conduit processor new` (cmd/conduit/root/connectors
// and cmd/conduit/root/processors each register a two-line wrapper that sets
// Kind and the command-specific Usage/Docs/Example — see NewCommand). Living
// here rather than duplicated in each subcommand package keeps the flag set,
// validation wiring, and human-output rendering identical between the two
// commands, which is exactly the property the CLI output conventions
// require ("the CLI reads as one product").
//
// This package is a thin cobra/ecdysis adapter over pkg/scaffold: it owns
// flags, argument parsing, and rendering (human text and the --json
// envelope via cecdysis.CommandWithResult), and delegates every actual
// scaffolding decision to scaffold.Generate. It has no business logic of
// its own to keep in sync with a future MCP `scaffold` tool, which is
// expected to call scaffold.Generate directly rather than go through this
// package (see pkg/scaffold's doc).
package scaffoldcmd
