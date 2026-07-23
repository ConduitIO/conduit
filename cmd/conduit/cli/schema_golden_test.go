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

// This file is the schema-golden test for v0.19 workstream 8 — see
// v019-plans/workstreams/cli-contract.md, which this test enforces rather
// than merely asserts. It is the harmonization guarantee for every other
// v0.19 workstream's --json surface (generate, embed, templates): any new
// command wired into root.RootCommand's tree is automatically classified the
// moment it registers, whether or not this file is ever touched again.
//
// # The two-family reality (cli-contract.md §4)
//
// There are two distinct, both-deliberate --json shapes in production, not
// one:
//
//   - Family A ("envelope"): cecdysis.CommandWithResult. Every --json output
//     is the shared Result envelope (command/ok/summary/result/error —
//     cmd/conduit/cecdysis/result.go), validated against the ONE committed
//     schema at cmd/conduit/cecdysis/testdata/envelope.schema.json. Today:
//     doctor, pipelines validate|lint|dry-run|repair|apply|deploy|init,
//     connectors audit|bundle|install|uninstall|new, processors new, init.
//   - Family B ("client-result passthrough"):
//     cecdysis.CommandWithExecuteWithClientResult. --json is the RAW result
//     value — a proto message via protojson (matching the HTTP API's JSON
//     shape) or a composite Go struct via go-json — never wrapped in the
//     envelope. This is confirmed correct, working, load-bearing behavior
//     (see cmd/conduit/root/pipelines/list_test.go's own doc comment), not a
//     bug or a migration backlog item. Today: pipelines
//     list|describe|inspect|start|stop, connectors list|describe, processors
//     list|describe, connectorplugins list|describe, processorplugins
//     list|describe.
//
// Forcing one schema across both was considered and rejected (cli-contract.md
// §5): Family B's payloads are heterogeneous proto/API messages whose shape is
// dictated by the gRPC/HTTP API contract, not this CLI layer — a schema loose
// enough to honestly cover both would be vacuous, and one strict enough to be
// useful (the envelope) is actively false for half of today's --json
// commands. Migrating Family B onto the envelope is a breaking change to
// already-consumed output (scripts, the MCP layer) and is explicitly out of
// scope for v0.19 (cli-contract.md §12, open question 1).
//
// # The one documented exception
//
// `quickstart --json` means "emit logs/records as JSON instead of
// human-readable text" (a streaming log-format toggle), not the envelope —
// see familyExceptions, below, and cli-contract.md §4.4.
package cli_test

import (
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/root"
	"github.com/conduitio/ecdysis"
)

// familyExceptions maps a leaf command's Usage() string to the reasoned
// justification for why it registers a --json flag without implementing
// either Family A or Family B. Every entry here is a deliberate, documented
// deviation — not a silent exemption — per cli-contract.md §4.5.
var familyExceptions = map[string]string{
	"quickstart": "quickstart --json means \"emit logs and records as JSON instead of " +
		"human-readable text\" (QuickstartFlags.JSON, cmd/conduit/root/quickstart/quickstart.go) " +
		"— a streaming log-format toggle, not the Result envelope. This is a known, " +
		"pre-existing violation of the org-wide --json flag vocabulary " +
		"(docs/design-documents/20260707-cli-output-conventions.md §3), left deliberately " +
		"unfixed for v0.19: quickstart is a long-running interactive demo command, " +
		"structurally unlike the one-shot commands this contract targets, and renaming its " +
		"--json flag (it can't become the envelope without a new flag name, since --json is " +
		"already taken) is a separate, larger discussion — see " +
		"v019-plans/workstreams/cli-contract.md §4.4 and §12, open question 2.",
}

// TestSchemaGolden_Completeness walks the real, live command tree —
// (&root.RootCommand{}).SubCommands(), recursively, exactly the same values
// cmd/conduit/cli.Run builds cobra commands from — and classifies every leaf
// command into Family A, Family B, or a named exception. A command that
// isn't wired into this tree doesn't exist as a CLI command at all, so
// there's no separate enumeration list to drift from it (cli-contract.md
// §5, alternative 3).
//
// The failure predicate (cli-contract.md §4.5): a leaf command FAILS this
// test if and only if it registers a --json flag AND implements neither
// Family A nor Family B AND is not in familyExceptions. This is what
// prevents a future command from hand-rolling --json output outside both
// blessed decorators without anyone noticing, while never flagging a
// genuinely --json-free, human-output-only command (version, run, mcp,
// docs, open — verified plain ecdysis.CommandWithExecute, no result
// interfaces, no --json flag of their own) — classification is by interface
// implementation, not by flag presence, so a command with no --json flag at
// all is correctly never flagged regardless of which interfaces it does or
// doesn't implement (cli-contract.md §7's edge-case table).
func TestSchemaGolden_Completeness(t *testing.T) {
	// The exact decorator set cmd/conduit/cli.Run wires (plus ecdysis's
	// DefaultDecorators, included automatically by ecdysis.New) — so a
	// --json flag this walk finds on a leaf is the same flag a real
	// `conduit <leaf>` invocation would expose, not an artifact of a
	// differently configured test harness.
	e := ecdysis.New(ecdysis.WithDecorators(
		cecdysis.CommandWithExecuteWithClientDecorator{},
		cecdysis.CommandWithExecuteWithClientResultDecorator{},
		cecdysis.CommandWithResultDecorator{},
	))

	familyA := 0
	familyB := 0
	exceptions := 0
	noJSON := 0

	var walk func(cmds []ecdysis.Command)
	walk = func(cmds []ecdysis.Command) {
		for _, c := range cmds {
			// A branch node (has its own subcommands, e.g. `pipelines`,
			// `connectors`) is not itself a --json leaf — recurse into its
			// children instead of classifying the branch.
			if sub, ok := c.(ecdysis.CommandWithSubCommands); ok {
				walk(sub.SubCommands())
				continue
			}

			switch c.(type) {
			case cecdysis.CommandWithResult:
				familyA++
				continue
			case cecdysis.CommandWithExecuteWithClientResult:
				familyB++
				continue
			}

			if reason, ok := familyExceptions[c.Usage()]; ok {
				t.Logf("classified %q as a documented exception: %s", c.Usage(), reason)
				exceptions++
				continue
			}

			// Neither family, not a named exception: build the real cobra
			// command (the same decorators production uses) and check
			// whether it actually exposes a --json flag. By construction,
			// neither client-result decorator attaches a --json flag to a
			// command implementing neither of their interfaces (see each
			// Decorate method's own type-assertion guard) — so the only way
			// a --json flag can appear here is a command defining its own
			// (e.g. quickstart's QuickstartFlags.JSON), which is exactly the
			// case this predicate exists to catch when undocumented.
			built := e.MustBuildCobraCommand(c)
			if built.Flags().Lookup("json") != nil {
				t.Errorf("command %q registers a --json flag but implements neither Family A "+
					"(cecdysis.CommandWithResult) nor Family B "+
					"(cecdysis.CommandWithExecuteWithClientResult), and is not a documented "+
					"exception in familyExceptions — classify it as one of the two families, "+
					"or add a reasoned exception entry (cli-contract.md §4.5)", c.Usage())
				continue
			}
			noJSON++
		}
	}

	walk((&root.RootCommand{}).SubCommands())

	total := familyA + familyB + exceptions + noJSON
	if total == 0 {
		t.Fatal("walk found zero commands — root.RootCommand's SubCommands() wiring is broken")
	}
	t.Logf("classified %d leaf commands: %d Family A, %d Family B, %d exceptions, %d no --json",
		total, familyA, familyB, exceptions, noJSON)

	// A regression guard on the walk itself: cli-contract.md §4.1/§4.2
	// verified 13 Family A and 13 Family B commands before this workstream
	// added init/pipelines-init to Family A (§6 AC-4), so today's true count
	// is 15 Family A + 13 Family B. If either count drops to zero, the walk
	// itself is broken (e.g. a decorator/interface mismatch), not the
	// product — fail loudly rather than silently "passing" with nothing
	// actually checked.
	if familyA == 0 {
		t.Error("zero Family A commands classified — the walk itself is almost certainly broken")
	}
	if familyB == 0 {
		t.Error("zero Family B commands classified — the walk itself is almost certainly broken")
	}
}
