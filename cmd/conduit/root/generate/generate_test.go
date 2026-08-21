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

package generate

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	gen "github.com/conduitio/conduit/cmd/conduit/internal/generate"
	"github.com/conduitio/conduit/cmd/conduit/internal/generate/provider"
	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

const validPipeline = `version: "2.2"
pipelines:
  - id: orders-to-log
    status: running
    connectors:
      - id: source
        type: source
        plugin: "builtin:generator"
        settings:
          format.type: structured
      - id: destination
        type: destination
        plugin: "builtin:log"
        settings:
          level: info
`

// fakeProvider returns a scripted reply without touching the network.
type fakeProvider struct {
	reply string
	seen  []provider.CompletionRequest
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Complete(_ context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	f.seen = append(f.seen, req)
	return provider.CompletionResult{Text: f.reply, TokensUsed: 11}, nil
}

// newTestCommand builds a Command wired to a fake provider and a fixed
// environment, so nothing in these tests reads real env or dials anything.
func newTestCommand(t *testing.T, prompt string, flags Flags, reply string) (*Command, *fakeProvider) {
	t.Helper()
	fake := &fakeProvider{reply: reply}
	cmd := &Command{
		args:  Args{Prompt: prompt},
		flags: flags,
		env: func(key string) string {
			if key == provider.EnvAnthropicKey {
				return "test-key"
			}
			return ""
		},
		newProvider: func(string, string, func(string) string) (provider.Provider, error) {
			return fake, nil
		},
		// No Ollama probing: a developer who happens to be running Ollama
		// locally would otherwise make these tests ambiguous-provider errors.
		probe: func(string) bool { return false },
	}
	return cmd, fake
}

// The security boundary is the ABSENCE of a way to apply, so it is asserted
// directly: a future flag that crosses it fails this test rather than quietly
// shipping.
func TestFlags_NoApplyPathExists(t *testing.T) {
	is := is.New(t)
	cmd := &Command{}

	var names []string
	for _, f := range cmd.Flags() {
		names = append(names, f.Long)
	}

	for _, forbidden := range []string{"yes", "deploy", "apply", "force-deploy", "run"} {
		is.True(!slices.Contains(names, forbidden))
	}
	slices.Sort(names)
	is.Equal(names, []string{"force", "max-retries", "model", "no-color", "out", "provider"})
}

func TestArgs(t *testing.T) {
	is := is.New(t)

	is.True((&Command{}).Args(nil) != nil)                                 // none
	is.True((&Command{}).Args([]string{"a", "b"}) != nil)                  // too many
	is.NoErr((&Command{}).Args([]string{"read from postgres into kafka"})) // exactly one
}

func TestExecuteWithResult_WritesAValidatedPipeline(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "orders.yaml")

	cmd, fake := newTestCommand(t, "generator to log", Flags{Out: out, MaxRetries: 3}, validPipeline)

	outcome, err := cmd.ExecuteWithResult(context.Background())
	is.NoErr(err)
	is.True(outcome.OK)

	result := outcome.Result.(Result)
	summary := outcome.Summary.(Summary)
	is.Equal(result.Path, out)
	is.Equal(summary.Attempts, 1)
	is.Equal(summary.Connectors, 2)
	is.Equal(summary.TokensUsed, 11)
	is.Equal(len(fake.seen), 1)

	written, err := os.ReadFile(out)
	is.NoErr(err)
	is.Equal(string(written), result.Pipeline)

	info, err := os.Stat(out)
	is.NoErr(err)
	is.Equal(info.Mode().Perm(), os.FileMode(0o600))
}

// A model-chosen filename is confined to the working directory; a
// user-supplied --out is taken as given. That asymmetry is the point.
func TestModelDerivedName_CannotEscapeTheWorkingDirectory(t *testing.T) {
	is := is.New(t)

	for _, tc := range []struct {
		id   string
		want string
	}{
		{"orders", "orders.yaml"},
		{"orders.yaml", "orders.yaml"},
		{"orders.yml", "orders.yml"},
		{"../../etc/passwd", "passwd.yaml"},
		{"/etc/passwd", "passwd.yaml"},
		{`..\..\windows\system32\config`, "config.yaml"},
		{"..", DefaultOutName},
		{"", DefaultOutName},
		{"   ", DefaultOutName},
		{"/", DefaultOutName},
	} {
		got := modelDerivedName(tc.id)
		is.Equal(got, tc.want)
		is.True(!strings.ContainsAny(got, `/\`)) // never a path, always a name
	}
}

func TestResolveOutPath_UserFlagIsTakenAsGiven(t *testing.T) {
	is := is.New(t)

	is.Equal(resolveOutPath("/tmp/mine.yaml", "orders"), "/tmp/mine.yaml")
	is.Equal(resolveOutPath("", "orders"), "orders.yaml")
}

// Overwriting is opt-in: a generated file landing on top of a hand-edited one
// would destroy work the user cannot recover.
func TestWriteCandidate_RefusesToClobberWithoutForce(t *testing.T) {
	is := is.New(t)
	path := filepath.Join(t.TempDir(), "p.yaml")
	is.NoErr(os.WriteFile(path, []byte("original\n"), 0o600))

	err := writeCandidate(path, "generated\n", false)
	is.True(err != nil)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.True(ce.Suggestion != "") // says how to proceed

	// The code is specific enough for a caller to act on WITHOUT reading the
	// message: "the file is in the way" (retry with --force or --out) is a
	// different fix from a generic invalid argument, which is why this is its
	// own code (design §8, amended).
	is.Equal(ce.Code, gen.CodeDestinationExists)
	// Additive, not breaking: AlreadyExists shares the Validation bucket with
	// InvalidArgument, so this case still exits 2 exactly as it did before the
	// code became specific.
	is.Equal(exitcode.ExitCode(err), exitcode.Validation)

	kept, err := os.ReadFile(path)
	is.NoErr(err)
	is.Equal(string(kept), "original\n")

	is.NoErr(writeCandidate(path, "generated\n", true))
	replaced, err := os.ReadFile(path)
	is.NoErr(err)
	is.Equal(string(replaced), "generated\n")
}

// A candidate that never validates surfaces the generation loop's coded
// error, and writes nothing.
func TestExecuteWithResult_InvalidCandidate_WritesNothing(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "orders.yaml")

	const noPlugin = `version: "2.2"
pipelines:
  - id: broken
    status: running
    connectors:
      - id: source
        type: source
`
	cmd, _ := newTestCommand(t, "generator to log", Flags{Out: out, MaxRetries: 2}, noPlugin)

	_, err := cmd.ExecuteWithResult(context.Background())
	is.True(err != nil)
	_, statErr := os.Stat(out)
	is.True(os.IsNotExist(statErr))
}

func TestNewProvider_BuildsEachAdapterFromItsOwnEnvVar(t *testing.T) {
	is := is.New(t)
	env := func(key string) string {
		switch key {
		case provider.EnvAnthropicKey:
			return "anthropic-key"
		case provider.EnvOpenAIKey:
			return "openai-key"
		default:
			return ""
		}
	}

	a, err := newProvider(provider.NameAnthropic, "m", env)
	is.NoErr(err)
	is.Equal(a.(*provider.Anthropic).APIKey, "anthropic-key")

	o, err := newProvider(provider.NameOpenAI, "m", env)
	is.NoErr(err)
	is.Equal(o.(*provider.OpenAI).APIKey, "openai-key")

	// Ollama has no key and falls back to the documented default host.
	l, err := newProvider(provider.NameOllama, "m", env)
	is.NoErr(err)
	is.Equal(l.(*provider.Ollama).Host, provider.DefaultOllamaHost)

	_, err = newProvider("nope", "m", env)
	is.True(err != nil)
}

// The rendered output shows the configuration, what is in it, and the same
// next steps a hand-written pipeline takes — including repair.
func TestRender_ShowsTheConfigurationAndTheNextSteps(t *testing.T) {
	is := is.New(t)
	cmd := &Command{}

	out := cmd.Render(cecdysis.Outcome{
		OK: true,
		Summary: Summary{
			Attempts: 1, MaxRetries: 3, Connectors: 2, Processors: 0, TokensUsed: 11,
		},
		Result: Result{
			Provider: "anthropic", Model: "claude-sonnet-5",
			Path: "orders.yaml", Pipeline: validPipeline,
		},
	})

	is.True(strings.Contains(out, "builtin:generator"))
	is.True(strings.Contains(out, "not deployed"))
	// Asserted against the real list, not a copy of it: a step quietly
	// dropped from nextSteps should fail this test, not pass a duplicate.
	is.Equal(strings.Join(nextSteps, " -> "), "validate -> dry-run -> repair -> deploy")
	for _, step := range nextSteps {
		is.True(strings.Contains(out, "conduit pipelines "+step+" orders.yaml"))
	}
}
