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
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/cecdysis"
	"github.com/conduitio/conduit/cmd/conduit/internal/generate/provider"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/ecdysis"
	"github.com/matryer/is"
)

// These tests run the real cobra command through ecdysis, so conduit.yaml is
// loaded by the same decorator chain a user's invocation goes through. They
// set process environment and change directory, so none of them is parallel.

// isolateEnv clears every variable that takes part in provider resolution or
// config loading, so the developer's own shell cannot decide the outcome.
func isolateEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		provider.EnvProvider,
		provider.EnvAnthropicKey,
		provider.EnvOpenAIKey,
		provider.EnvOllamaHost,
		"CONDUIT_CONFIG_PATH",
	} {
		t.Setenv(k, "")
	}
}

func writeConduitYAML(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "conduit.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runGenerate executes `conduit generate` end to end with a fake provider and
// returns the provider name the command resolved.
func runGenerate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var resolved string
	c := &Command{
		newProvider: func(name, _ string, _ func(string) string) (provider.Provider, error) {
			resolved = name
			return &fakeProvider{reply: validPipeline}, nil
		},
		probe: func(string) bool { return false },
	}
	e := ecdysis.New(ecdysis.WithDecorators(cecdysis.CommandWithResultDecorator{}))
	cmd := e.MustBuildCobraCommand(c)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{
		"generator to log",
		"--out", filepath.Join(t.TempDir(), "out.yaml"),
	}, args...))
	_, err := cmd.ExecuteC()
	return resolved, err
}

// TestProviderPrecedence pins --provider > CONDUIT_GENERATE_PROVIDER >
// generate.provider in conduit.yaml > auto-detect. Each case sets every lower
// source to a different provider, so it only passes if the higher one wins.
func TestProviderPrecedence(t *testing.T) {
	t.Run("flag beats env and config", func(t *testing.T) {
		is := is.New(t)
		isolateEnv(t)
		dir := t.TempDir()
		t.Chdir(dir)
		writeConduitYAML(t, dir, "generate:\n  provider: anthropic\n")
		t.Setenv(provider.EnvProvider, provider.NameOllama)

		got, err := runGenerate(t, "--provider", provider.NameOpenAI)
		is.NoErr(err)
		is.Equal(got, provider.NameOpenAI)
	})

	t.Run("env beats config", func(t *testing.T) {
		is := is.New(t)
		isolateEnv(t)
		dir := t.TempDir()
		t.Chdir(dir)
		writeConduitYAML(t, dir, "generate:\n  provider: anthropic\n")
		t.Setenv(provider.EnvProvider, provider.NameOllama)

		got, err := runGenerate(t)
		is.NoErr(err)
		is.Equal(got, provider.NameOllama)
	})

	t.Run("config beats auto-detect", func(t *testing.T) {
		is := is.New(t)
		isolateEnv(t)
		dir := t.TempDir()
		t.Chdir(dir)
		writeConduitYAML(t, dir, "generate:\n  provider: openai\n")
		// Auto-detect alone would pick anthropic.
		t.Setenv(provider.EnvAnthropicKey, "sk-ant")

		got, err := runGenerate(t)
		is.NoErr(err)
		is.Equal(got, provider.NameOpenAI)
	})

	t.Run("auto-detect when nothing is explicit", func(t *testing.T) {
		is := is.New(t)
		isolateEnv(t)
		t.Chdir(t.TempDir()) // no conduit.yaml at all
		t.Setenv(provider.EnvAnthropicKey, "sk-ant")

		got, err := runGenerate(t)
		is.NoErr(err)
		is.Equal(got, provider.NameAnthropic)
	})

	t.Run("config.path points at a conduit.yaml elsewhere", func(t *testing.T) {
		is := is.New(t)
		isolateEnv(t)
		t.Chdir(t.TempDir())
		path := writeConduitYAML(t, t.TempDir(), "generate:\n  provider: ollama\n")
		t.Setenv(provider.EnvAnthropicKey, "sk-ant")

		got, err := runGenerate(t, "--config.path", path)
		is.NoErr(err)
		is.Equal(got, provider.NameOllama)
	})
}

// TestAmbiguousProvider_AdviceResolvesIt is the #2908 regression test: the
// ambiguous-provider error tells the user to set --provider,
// CONDUIT_GENERATE_PROVIDER or generate.provider in conduit.yaml. Each piece
// of advice, followed literally, must resolve the error. Before the fix,
// generate.provider was never read and following that advice returned the
// same error again.
func TestAmbiguousProvider_AdviceResolvesIt(t *testing.T) {
	setup := func(t *testing.T) string {
		t.Helper()
		isolateEnv(t)
		dir := t.TempDir()
		t.Chdir(dir)
		t.Setenv(provider.EnvAnthropicKey, "sk-ant")
		t.Setenv(provider.EnvOpenAIKey, "sk-oai")
		return dir
	}

	is := is.New(t)
	setup(t)
	_, err := runGenerate(t)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, provider.CodeAmbiguousProvider)
	for _, advice := range []string{"--provider", provider.EnvProvider, "generate.provider in conduit.yaml"} {
		is.True(strings.Contains(ce.Suggestion, advice)) // the suggestion names each fix
	}

	t.Run("generate.provider in conduit.yaml", func(t *testing.T) {
		is := is.New(t)
		dir := setup(t)
		writeConduitYAML(t, dir, "generate:\n  provider: openai\n")

		got, err := runGenerate(t)
		is.NoErr(err)
		is.Equal(got, provider.NameOpenAI)
	})

	t.Run(provider.EnvProvider, func(t *testing.T) {
		is := is.New(t)
		setup(t)
		t.Setenv(provider.EnvProvider, provider.NameOpenAI)

		got, err := runGenerate(t)
		is.NoErr(err)
		is.Equal(got, provider.NameOpenAI)
	})

	t.Run("--provider", func(t *testing.T) {
		is := is.New(t)
		setup(t)

		got, err := runGenerate(t, "--provider", provider.NameOpenAI)
		is.NoErr(err)
		is.Equal(got, provider.NameOpenAI)
	})
}

// An unknown name in conduit.yaml fails with a coded error that points at the
// config key, not with a silent fall-through to auto-detection.
func TestProviderConfig_UnknownNameIsRejected(t *testing.T) {
	is := is.New(t)
	isolateEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)
	writeConduitYAML(t, dir, "generate:\n  provider: antropic\n")
	t.Setenv(provider.EnvAnthropicKey, "sk-ant")

	got, err := runGenerate(t)
	is.Equal(got, "") // no provider was built
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
	is.Equal(ce.ConfigPath, "/generate/provider")
	is.True(strings.Contains(ce.Message, `"antropic"`))
}
