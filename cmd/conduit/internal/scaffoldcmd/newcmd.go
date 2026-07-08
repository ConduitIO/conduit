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

package scaffoldcmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/ui"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/scaffold"
	"github.com/conduitio/ecdysis"
)

// NewCommand implements every part of cecdysis.CommandWithResult except
// Usage/Docs (ecdysis.Command's base Usage and ecdysis.CommandWithDocs),
// which the concrete per-kind wrapper supplies — see the compile-time
// interface assertions in cmd/conduit/root/connectors/new.go and
// cmd/conduit/root/processors/new.go, not here.
//
// Flags are the flags shared by `connector new` and `processor new`, per
// this command's design doc.
type Flags struct {
	Lang         string `long:"lang" usage:"target language; only \"go\" is available today"`
	Module       string `long:"module" usage:"Go module path (required)"` // overridden per-kind in Flags(), below
	Path         string `long:"path" usage:"destination directory (default: ./conduit-<kind>-<name>)"`
	SDKVersion   string `long:"sdk-version" usage:"override the SDK version pinned in the embedded template"`
	Git          bool   `long:"git" usage:"initialize a git repository and create the first commit"`
	NoGit        bool   `long:"no-git" usage:"skip git initialization"`
	SkipGenerate bool   `long:"skip-generate" usage:"skip installing the code-gen tool and running code generation (both templates ship pre-generated output, so the result still builds)"`
	Yes          bool   `long:"yes" short:"y" usage:"confirm without prompting (currently always the case: interactive confirmation is not implemented yet — accepted for forward compatibility with the CLI output conventions' mutating-command flag set)"`
	Force        bool   `long:"force" usage:"overwrite the destination directory if it already exists"`
}

// NewCommand is the shared implementation behind `connector new` and
// `processor new`. Kind is the only thing that differs between the two
// concrete commands (see cmd/conduit/root/connectors/new.go and
// cmd/conduit/root/processors/new.go).
type NewCommand struct {
	Kind scaffold.Kind

	flags Flags
	name  string
}

// ResultCommand returns the --json envelope's stable dotted discriminator.
func (c *NewCommand) ResultCommand() string {
	return string(c.Kind) + ".new"
}

func (c *NewCommand) Flags() []ecdysis.Flag {
	flags := ecdysis.BuildFlags(&c.flags)
	// --git defaults to true (git init runs unless explicitly opted out via
	// --git=false or --no-git). ecdysis.BuildFlags has no struct-tag support
	// for a non-zero-value bool default (see its Default field's doc — a
	// tag-declared default isn't read; only SetDefault after the fact is),
	// so this is the documented way to get a true default through it.
	flags.SetDefault("git", true)
	// --lang defaults to "go", the only language ready today (see the design
	// doc's feasibility verdict) — --lang python is still accepted (and
	// still gated by scaffold.validate) so the honest "not yet" error fires
	// on an explicit request instead of silently substituting go.
	flags.SetDefault("lang", string(scaffold.LanguageGo))

	// --module's usage text is kind-specific ("conduit-connector-<name>" vs
	// "conduit-processor-<name>") but the struct tag it's built from (Flags,
	// above) is shared between both commands — patch it here, after
	// BuildFlags, rather than forking the whole Flags struct per kind for
	// one string.
	for i, f := range flags {
		if f.Long == "module" {
			flags[i].Usage = fmt.Sprintf("Go module path, e.g. github.com/<you>/conduit-%s-<name> (required)", c.Kind)
		}
	}
	return flags
}

func (c *NewCommand) Args(args []string) error {
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments")
	}
	if len(args) == 1 {
		c.name = args[0]
	}
	// A missing name is intentionally not an error here: scaffold.Generate's
	// own validation raises scaffold.CodeInvalidModule for it, which — unlike
	// an error returned from Args (rendered by cobra's own default, undecorated
	// error handling; see the CLI output conventions gap this command's design
	// doc flags for CommandWithArgs) — gets the full --json envelope /
	// ui.RenderFinding treatment via ExecuteWithResult's normal error path.
	return nil
}

// Example renders `conduit <kind> new <kind-name> [flags]` style usage text
// for Docs.Example, shared by both concrete commands so the two examples
// only differ in the noun.
func Example(kind scaffold.Kind, exampleName string) string {
	return fmt.Sprintf(
		"conduit %[1]s new %[2]s --module github.com/you/conduit-%[1]s-%[2]s\n"+
			"conduit %[1]s new %[2]s --yes --json",
		kind, exampleName,
	)
}

func (c *NewCommand) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	req := scaffold.Request{
		Kind:         c.Kind,
		Language:     scaffold.Language(c.flags.Lang),
		Name:         c.name,
		Module:       c.flags.Module,
		Path:         c.flags.Path,
		SDKVersion:   c.flags.SDKVersion,
		Git:          c.flags.Git && !c.flags.NoGit,
		SkipGenerate: c.flags.SkipGenerate,
		Force:        c.flags.Force,
	}

	res, err := scaffold.Generate(ctx, req)
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	failedSteps := 0
	for _, s := range res.Steps {
		if !s.OK {
			failedSteps++
		}
	}

	return cecdysis.Outcome{
		OK: true,
		Summary: map[string]any{
			"stepsTotal":  len(res.Steps),
			"stepsFailed": failedSteps,
		},
		Result: res,
	}, nil
}

// stepLabels renders a StepResult's Name into the human-readable line the
// design doc's UX mockup shows (e.g. "Rewrote module path" rather than the
// machine name "rewrite_module"). generateLabel is resolved per-kind (see
// Render) since "generate" means something different for a connector
// (connector.yaml + README, via conn-sdk-cli) than a processor
// (paramgen_proc.go, via paramgen) — the same asymmetry package steps
// documents.
func stepLabel(kind scaffold.Kind, name string) string {
	switch name {
	case scaffold.StepToolchain:
		return "Toolchain OK"
	case scaffold.StepExtractTemplate:
		return "Wrote template files"
	case scaffold.StepRewriteModule:
		return "Rewrote module path"
	case scaffold.StepSDKVersion:
		return "Set SDK version"
	case scaffold.StepInstallTools:
		return "Installed code-gen tool"
	case scaffold.StepGenerate:
		if kind == scaffold.KindProcessor {
			return "Generated paramgen_proc.go"
		}
		return "Generated connector.yaml + README"
	case scaffold.StepBuild:
		return "go build OK"
	case scaffold.StepGitInit:
		return "Initialized git, created first commit"
	default:
		return name
	}
}

// Render implements cecdysis.CommandWithResult. It is only called for a
// successful run (see that interface's doc) — a hard failure is rendered by
// the shared decorator itself, via ui.RenderFinding, before Render would
// ever be reached.
func (c *NewCommand) Render(outcome cecdysis.Outcome) string {
	res, ok := outcome.Result.(scaffold.Result)
	if !ok {
		return ""
	}

	r := ui.NewRenderer(os.Stdout, false)
	var b strings.Builder

	for _, s := range res.Steps {
		glyph := ui.StatusPass
		if !s.OK {
			glyph = ui.StatusFail // git_init is best-effort and can be OK:false without failing the run
		}
		label := stepLabel(res.Kind, s.Name)
		if s.Name == scaffold.StepRewriteModule {
			label += " → " + res.Module
		}
		fmt.Fprintf(&b, "%s %s (%s)\n", r.Glyph(glyph), label, formatDuration(s.DurationMS))
	}

	fmt.Fprintf(&b, "Your %s is ready at %s  (%s)\n", res.Kind, res.Path, formatDuration(res.ElapsedMS))
	b.WriteString("Next steps:\n")
	for _, step := range res.NextSteps {
		b.WriteString("  " + step + "\n")
	}

	return b.String()
}

// formatDuration renders milliseconds the way the design doc's UX mockup
// does: sub-second as "594ms", one second or more as "5.6s".
func formatDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return strconv.FormatInt(ms, 10) + "ms"
	}
	return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
}
