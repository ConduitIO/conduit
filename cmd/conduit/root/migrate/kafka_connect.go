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

package migrate

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/ui"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/migrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithResultExitCode = (*KafkaConnectCommand)(nil)
	_ ecdysis.CommandWithDocs            = (*KafkaConnectCommand)(nil)
	_ ecdysis.CommandWithFlags           = (*KafkaConnectCommand)(nil)
	_ ecdysis.CommandWithArgs            = (*KafkaConnectCommand)(nil)
)

// KafkaConnectFlags are the flags of `conduit migrate kafka-connect`. The
// offset-import, cutover, REST and registry flags of the design arrive in
// later PRs.
type KafkaConnectFlags struct {
	Out        string `long:"out" usage:"output directory" default:"./conduit-migration"`
	Force      bool   `long:"force" usage:"write into a non-empty --out directory, overwriting the migration files in it"`
	DryRun     bool   `long:"dry-run" usage:"produce the report only and write no files"`
	Worker     string `long:"worker" usage:"Kafka Connect worker properties file that supplies defaults (converters, client security)"`
	Format     string `long:"format" usage:"input format instead of detection: properties, json or rest-export"`
	Strict     bool   `long:"strict" usage:"also fail (exit 2) on approximated entries and accepted gaps"`
	AcceptGaps bool   `long:"accept-gaps" usage:"do not fail on blocking entries; record them in the report's acceptedGaps"`
	Verbose    bool   `long:"verbose" usage:"also list keys that were translated with no remark"`
	Quiet      bool   `long:"quiet" short:"q" usage:"print only findings and the summary"`
	NoColor    bool   `long:"no-color" usage:"disable colored/glyph output even on a color-capable terminal"`
}

// KafkaConnectCommand implements `conduit migrate kafka-connect`.
type KafkaConnectCommand struct {
	args  []string
	flags KafkaConnectFlags

	stdin    io.Reader
	renderer *ui.Renderer
	wrote    bool
	removed  []string
}

func (c *KafkaConnectCommand) Usage() string { return "kafka-connect <input>..." }

func (c *KafkaConnectCommand) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Translate Kafka Connect connectors into Conduit pipelines and a compatibility report",
		Long: `Reads Kafka Connect connector configs and writes, for each connector, a Conduit pipeline
and a compatibility report that lists every input key as translated, approximated (with the
difference) or unsupported (with the fix). No key is dropped: a key no rule knows is reported
as kc.key_unknown and blocks the migration.

Inputs are files, directories (their .json and .properties files) or - for standard input, in
any of these forms: a .properties file, a connector as the Connect REST API takes or returns it,
a bare config map, or the output of GET /connectors?expand=info&expand=status.

The command is offline: it reads its inputs and writes --out, nothing else. It exits 0 when no
entry blocks, 2 when one does (or on bad input), 3 when an output file cannot be written, and
1 on an internal error. With --dry-run it writes nothing and exits the same way. --force
replaces the files of an earlier run in --out, including pipeline files for connectors this
run no longer has; files migrate did not write are left alone.

Connector classes this version migrates: ` + classList() + `.`,
		Example: "conduit migrate kafka-connect connectors/orders.json\n" +
			"conduit migrate kafka-connect --worker connect-distributed.properties ./connectors --dry-run\n" +
			"curl -s localhost:8083/connectors?expand=info | conduit migrate kafka-connect - --json",
	}
}

func classList() string {
	e, err := rules.Builtin()
	if err != nil || len(e.Classes()) == 0 {
		return "none yet"
	}
	return strings.Join(e.Classes(), ", ")
}

func (c *KafkaConnectCommand) Flags() []ecdysis.Flag { return ecdysis.BuildFlags(&c.flags) }

// Args records the inputs. No input is checked in ExecuteWithResult, so it
// is a coded usage error with an envelope under --json instead of cobra's
// silent exit.
func (c *KafkaConnectCommand) Args(args []string) error {
	c.args = args
	return nil
}

func (c *KafkaConnectCommand) ResultCommand() string { return "migrate.kafka-connect" }

func (c *KafkaConnectCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	out := io.Discard
	stdin := c.stdin
	if cmd := ecdysis.CobraCmdFromContext(ctx); cmd != nil {
		out = cmd.OutOrStdout()
		if stdin == nil {
			stdin = cmd.InOrStdin()
		}
	}
	if stdin == nil {
		stdin = os.Stdin
	}
	c.renderer = ui.NewRenderer(out, c.flags.NoColor)

	if len(c.args) == 0 {
		ce := conduiterr.New(kcmigrate.CodeInputUnrecognized, "no input: pass at least one connector config")
		ce.Suggestion = "pass files, directories, or - for standard input, for example: conduit migrate kafka-connect connectors/"
		return cecdysis.Outcome{}, ce
	}
	format, err := kcmigrate.ParseFormat(c.flags.Format)
	if err != nil {
		return cecdysis.Outcome{}, err
	}
	inputs, worker, err := readInputs(c.args, c.flags.Worker, stdin)
	if err != nil {
		return cecdysis.Outcome{}, err
	}
	engine, err := rules.Builtin()
	if err != nil {
		return cecdysis.Outcome{}, conduiterr.Wrap(conduiterr.CodeInternal, "the embedded migration rules are invalid", err)
	}
	res, err := migrate.Run(ctx, engine, migrate.Request{
		Inputs: inputs, Worker: worker, Format: format,
		Strict: c.flags.Strict, AcceptGaps: c.flags.AcceptGaps,
		ConduitVersion: conduit.Version(false),
	})
	if err != nil {
		return cecdysis.Outcome{}, err
	}
	if !c.flags.DryRun {
		removed, err := writeOutputs(c.flags.Out, res.Files, c.flags.Force)
		if err != nil {
			return cecdysis.Outcome{}, err
		}
		c.removed = removed
		c.wrote = true
	}
	return cecdysis.Outcome{OK: res.OK, Summary: res.Report.Summary, Result: res.Report}, nil
}

// ExitCode reduces the findings to the exit code: 2 (validation) when the
// run is not OK, else 0.
func (c *KafkaConnectCommand) ExitCode(outcome cecdysis.Outcome) int {
	if outcome.OK {
		return exitcode.OK
	}
	return exitcode.Validation
}

// Render prints one block per connector with its findings (every entry that
// is not plainly translated, all entries with --verbose), then the summary
// and one line saying what to do next.
func (c *KafkaConnectCommand) Render(outcome cecdysis.Outcome) string {
	rep, _ := outcome.Result.(*kcmigrate.Report)
	if rep == nil {
		return ""
	}
	if c.renderer == nil {
		c.renderer = ui.NewRenderer(io.Discard, true)
	}
	var b strings.Builder
	for _, rc := range rep.Connectors {
		var shown []kcmigrate.Entry
		for _, e := range rc.Entries {
			if c.flags.Verbose || e.Status != kcmigrate.StatusTranslated || e.Code != "" || e.Blocking {
				shown = append(shown, e)
			}
		}
		if len(shown) == 0 && len(rc.Notes) == 0 && c.flags.Quiet {
			continue
		}
		fmt.Fprintf(&b, "%s  %s  ->  %s\n", rc.Name, rc.Class, targetText(rc))
		for _, e := range shown {
			ui.RenderFinding(&b, c.glyph(e.Status, e.Blocking), e.Code, e.Key, e.Origin.String()+": "+e.Reason, e.Fix)
		}
		for _, n := range rc.Notes {
			ui.RenderFinding(&b, c.glyph(kcmigrate.StatusApproximated, n.Blocking), n.Code, "", n.Reason, n.Fix)
		}
		fmt.Fprintln(&b)
	}
	s := rep.Summary
	fmt.Fprintf(&b, "Summary: %d keys · %d translated · %d approximated · %d unsupported · %d blocking\n",
		s.Keys, s.Translated, s.Approximated, s.Unsupported, s.Blocking)
	switch {
	case !outcome.OK && s.Blocking > 0 && !c.flags.AcceptGaps:
		fmt.Fprintln(&b, "Fix the ✗ items above, then re-run.")
	case !outcome.OK:
		fmt.Fprintln(&b, "--strict: the ⚠ items above and any accepted gaps fail the run.")
	}
	if !c.flags.Quiet {
		c.renderFiles(&b)
	}
	return b.String()
}

func (c *KafkaConnectCommand) glyph(status kcmigrate.Status, blocking bool) string {
	switch {
	case blocking:
		return c.renderer.Glyph(ui.StatusFail)
	case status == kcmigrate.StatusTranslated:
		return c.renderer.Glyph(ui.StatusPass)
	default:
		return c.renderer.Glyph(ui.StatusWarn)
	}
}

func targetText(rc kcmigrate.ReportConnector) string {
	switch {
	case rc.Role == kcmigrate.RoleWorker:
		return "worker settings"
	case rc.Pipeline == "":
		return "no pipeline"
	case rc.Target != nil:
		return fmt.Sprintf("%s source, %s destination (%s)", rc.Target.Source, rc.Target.Destination, rc.PipelineStatus)
	default:
		return rc.Pipeline
	}
}

// renderFiles says what the run wrote and removed.
func (c *KafkaConnectCommand) renderFiles(b *strings.Builder) {
	if len(c.removed) > 0 {
		fmt.Fprintf(b, "Removed %d files of an earlier run from %s: %s.\n", len(c.removed), c.flags.Out, strings.Join(c.removed, ", "))
	}
	if c.wrote {
		fmt.Fprintf(b, "Wrote %s and %s to %s.\n", migrate.ReportFile, migrate.ReportMarkdownFile, c.flags.Out)
	} else {
		fmt.Fprintln(b, "Dry run: no files written.")
	}
}
