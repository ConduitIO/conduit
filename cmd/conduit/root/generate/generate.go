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

// Package generate implements `conduit generate "<natural language>"`: it
// turns a description of a pipeline into a validated pipeline configuration
// file.
//
// # The boundary that defines this command
//
// Generation never deploys, applies, or mutates anything. Its only side
// effects are one provider call and one file write, and the file write is
// confined to the working directory whenever the filename came from the model
// (outpath.go). There is deliberately no --yes, --deploy, or --apply flag: the
// boundary is enforced by the ABSENCE of a way to cross it, not by a
// confirmation prompt a script could answer. Deploying a generated pipeline is
// the same explicit `conduit pipelines deploy` step a hand-written one needs.
//
// Every configuration this command shows or writes has passed the same
// validate engine `conduit pipelines validate` runs — see
// cmd/conduit/internal/generate, which owns the grounded generation loop this
// package is a CLI shell over.
package generate

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	gen "github.com/conduitio/conduit/cmd/conduit/internal/generate"
	"github.com/conduitio/conduit/cmd/conduit/internal/generate/provider"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/ecdysis"
)

var (
	_ cecdysis.CommandWithResult = (*Command)(nil)
	_ ecdysis.CommandWithDocs    = (*Command)(nil)
	_ ecdysis.CommandWithFlags   = (*Command)(nil)
	_ ecdysis.CommandWithArgs    = (*Command)(nil)
)

// Args holds the natural-language request.
type Args struct {
	Prompt string
}

// Flags holds the command's flags.
//
// There is no --yes/--deploy/--apply here, and that absence is load-bearing
// (see the package doc). A flag-set golden test asserts it, because a boundary
// enforced by an absence needs a test that notices when the absence ends.
type Flags struct {
	Provider   string `long:"provider" usage:"generation provider to use (anthropic, openai, ollama); auto-detected when exactly one is configured"`
	Model      string `long:"model" usage:"provider-specific model identifier; the provider's default when unset"`
	Out        string `long:"out" usage:"path to write the generated pipeline to; defaults to a filename derived from the pipeline id, in the working directory"`
	MaxRetries int    `long:"max-retries" usage:"how many provider calls a single generate may make, including the first" default:"3"`
	Force      bool   `long:"force" usage:"overwrite the output file if it already exists"`
	NoColor    bool   `long:"no-color" usage:"disable colored/glyph output even on a color-capable terminal"`
}

// Command implements `conduit generate`.
type Command struct {
	args  Args
	flags Flags

	// env and newProvider are seams so a test can exercise the whole command
	// — resolve, generate, write, render — without touching process
	// environment or making a network call. Nil means the real thing.
	env         func(string) string
	newProvider func(name, model string, env func(string) string) (provider.Provider, error)
	// probe reports whether a local Ollama server is reachable. It is a seam
	// for the same reason provider.Probe is one: auto-detection would
	// otherwise make a network call from every test that constructs this
	// command, and whether the developer happens to be running Ollama would
	// decide whether those tests pass.
	probe provider.Probe
}

func (c *Command) Usage() string { return "generate <request>" }

func (c *Command) Docs() ecdysis.Docs {
	return ecdysis.Docs{
		Short: "Generate a pipeline configuration from a natural-language description",
		Long: `Turns a description of a pipeline into a pipeline configuration file.

Every generated configuration is checked with the same offline validate engine
'conduit pipelines validate' uses, including that every connector it references actually
exists, before it is shown or written. A configuration that does not pass is never
emitted; the command retries with the specific findings and, if it still cannot produce a
valid pipeline, reports what was wrong.

This command never deploys anything. It writes a file; deploying it is the same explicit
'conduit pipelines deploy' step a hand-written pipeline needs.

A generation provider must be configured: set ANTHROPIC_API_KEY or OPENAI_API_KEY, or run a
local Ollama server. When more than one is available, choose with --provider rather than
letting the command pick for you.`,
		Example: `conduit generate "read from postgres and write new rows to s3 as json"
conduit generate "stream orders from postgres into kafka, only orders over 100" --out orders.yaml
conduit generate "sync kafka into postgres" --provider anthropic --json`,
	}
}

func (c *Command) Flags() []ecdysis.Flag { return ecdysis.BuildFlags(&c.flags) }

func (c *Command) Args(args []string) error {
	if len(args) == 0 {
		return cerrors.Errorf("requires a description of the pipeline to generate")
	}
	if len(args) > 1 {
		return cerrors.Errorf("too many arguments: quote the description as a single argument")
	}
	c.args.Prompt = args[0]
	return nil
}

func (c *Command) ResultCommand() string { return "generate" }

// Result is the --json payload (design §6).
type Result struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Path is where the configuration was written.
	Path string `json:"path"`
	// Pipeline is the configuration itself, so a consumer never has to read
	// the file back to see what was produced.
	Pipeline string `json:"pipeline"`
}

// Summary is the --json summary block.
type Summary struct {
	Attempts   int `json:"attempts"`
	MaxRetries int `json:"maxRetries"`
	Connectors int `json:"connectors"`
	Processors int `json:"processors"`
	// TokensUsed is what the provider reported, summed across attempts, or 0
	// when it reported nothing. Never estimated — this is a number users may
	// bill against.
	TokensUsed int `json:"tokensUsed"`
}

// ExecuteWithResult resolves a provider, generates a validated pipeline, and
// writes it.
//
// Errors are returned rather than folded into the envelope as OK:false: unlike
// `validate`, which reports findings about files that exist, every failure
// here means no artifact was produced, and each carries a registered code from
// design §8 whose gRPC class maps to the process exit code through
// pkg/conduit/exitcode.
func (c *Command) ExecuteWithResult(ctx context.Context) (cecdysis.Outcome, error) {
	env := c.env
	if env == nil {
		env = os.Getenv
	}

	probe := c.probe
	if probe == nil {
		probe = probeOllama
	}

	name, err := provider.Resolve(provider.ResolveInput{
		Flag:        c.flags.Provider,
		Env:         env,
		ProbeOllama: probe,
	})
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	build := c.newProvider
	if build == nil {
		build = newProvider
	}
	p, err := build(name, c.flags.Model, env)
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	generated, err := gen.Generate(ctx, gen.Input{
		Prompt:      c.args.Prompt,
		Provider:    p,
		Model:       c.flags.Model,
		MaxAttempts: c.flags.MaxRetries,
	})
	if err != nil {
		return cecdysis.Outcome{}, err
	}

	summary := gen.Summarize(ctx, generated.Candidate)
	path := resolveOutPath(c.flags.Out, summary.PipelineID)
	if err := writeCandidate(path, generated.Candidate, c.flags.Force); err != nil {
		return cecdysis.Outcome{}, err
	}

	return cecdysis.Outcome{
		OK: true,
		Summary: Summary{
			Attempts:   len(generated.Attempts),
			MaxRetries: c.flags.MaxRetries,
			Connectors: summary.Connectors,
			Processors: summary.Processors,
			TokensUsed: generated.TokensUsed,
		},
		Result: Result{
			Provider: name,
			Model:    c.flags.Model,
			Path:     path,
			Pipeline: generated.Candidate,
		},
	}, nil
}

// Render is the human output: the configuration, what is in it, and the
// commands that take it further.
//
// The footer always lists validate, dry-run, repair, and deploy in that order
// — the same path a hand-written configuration takes, including `repair` for a
// finding that has a fix.
func (c *Command) Render(outcome cecdysis.Outcome) string {
	result, _ := outcome.Result.(Result)
	summary, _ := outcome.Summary.(Summary)

	var b strings.Builder
	fmt.Fprintf(&b, "Generated with %s", result.Provider)
	if result.Model != "" {
		fmt.Fprintf(&b, " (%s)", result.Model)
	}
	fmt.Fprintf(&b, ", attempt %d of %d. Validation passed.\n\n", summary.Attempts, summary.MaxRetries)

	b.WriteString(result.Pipeline)
	if !strings.HasSuffix(result.Pipeline, "\n") {
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "\n%d connector(s), %d processor(s)", summary.Connectors, summary.Processors)
	if summary.TokensUsed > 0 {
		fmt.Fprintf(&b, " · %d tokens", summary.TokensUsed)
	}
	fmt.Fprintf(&b, "\nWrote %s (not deployed).\n", result.Path)

	b.WriteString("\nNext steps:\n")
	for _, step := range nextSteps {
		fmt.Fprintf(&b, "  conduit pipelines %s %s\n", step, result.Path)
	}
	return b.String()
}

// writeCandidate writes the configuration, refusing to clobber an existing
// file unless --force was given.
//
// 0600 matches `pipelines init`: a generated configuration commonly carries
// connection strings the user is about to fill in, and a file that starts
// private can always be relaxed.
func writeCandidate(path, candidate string, force bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}

	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if os.IsExist(err) {
			// Deliberately a FOUNDATIONAL code rather than a new
			// generate.destination_exists: design §8's taxonomy is a frozen
			// contract that does not list one for this case, and the provider
			// package took the same decision for its unknown-provider case
			// (codeUnknownProvider). If a dedicated code is wanted it belongs
			// in §8 first, then here.
			e := conduiterr.Wrap(conduiterr.CodeInvalidArgument,
				fmt.Sprintf("%s already exists", path), err)
			e.Suggestion = "pass --force to overwrite it, or --out to write somewhere else"
			return e
		}
		return conduiterr.Wrap(conduiterr.CodeInternal,
			fmt.Sprintf("could not write %s: %v", path, err), err)
	}
	defer f.Close()

	if _, err := f.WriteString(candidate); err != nil {
		return conduiterr.Wrap(conduiterr.CodeInternal,
			fmt.Sprintf("could not write %s: %v", path, err), err)
	}
	return nil
}

// newProvider builds the adapter for a resolved provider name.
//
// Credentials are read here and never logged, never echoed into the prompt,
// and never put in --json output.
func newProvider(name, model string, env func(string) string) (provider.Provider, error) {
	switch name {
	case provider.NameAnthropic:
		return &provider.Anthropic{APIKey: env(provider.EnvAnthropicKey), Model: model}, nil
	case provider.NameOpenAI:
		return &provider.OpenAI{APIKey: env(provider.EnvOpenAIKey), Model: model}, nil
	case provider.NameOllama:
		host := env(provider.EnvOllamaHost)
		if strings.TrimSpace(host) == "" {
			host = provider.DefaultOllamaHost
		}
		return &provider.Ollama{Host: host, Model: model}, nil
	default:
		// Unreachable: Resolve only ever returns a name it validated. Coded
		// rather than panicking, because an unreachable branch that becomes
		// reachable should tell the user something actionable.
		e := conduiterr.New(conduiterr.CodeInternal,
			fmt.Sprintf("resolved unknown generation provider %q", name))
		e.Suggestion = "select one explicitly with --provider: " + strings.Join(provider.Names, ", ")
		return nil, e
	}
}

// probeOllama reports whether a local Ollama server is reachable, with a short
// timeout: this runs on every invocation that has no explicit provider, so it
// must never be what makes the command feel slow.
func probeOllama(host string) bool {
	return provider.Reachable(host, &http.Client{Timeout: probeTimeout}, probeTimeout)
}

const probeTimeout = 500 * time.Millisecond

// nextSteps is the footer, in the order a user actually walks it: check it,
// see what it would do, fix anything fixable, then run it. `repair` is in the
// list because a generated file with a fixable finding routes to the same
// repair path a hand-written one does.
var nextSteps = []string{"validate", "dry-run", "repair", "deploy"}
