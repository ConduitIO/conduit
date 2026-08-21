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
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gen "github.com/conduitio/conduit/cmd/conduit/internal/generate"
	"github.com/conduitio/conduit/cmd/conduit/internal/generate/provider"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// adversarialFixturesPath reaches the ONE committed corpus
// (cmd/conduit/internal/generate/testdata/adversarial_requests.yaml) from
// this package's own test working directory. There is deliberately no
// second copy of the fixture data in this package — a copy would be free to
// drift from what the internal package's own injection tests exercise.
const adversarialFixturesPath = "../../internal/generate/testdata/adversarial_requests.yaml"

// validPostgresToKafkaPipeline is the benign, valid reply used for
// adversarial fixtures whose PROMPT names postgres and kafka explicitly
// ("read from postgres and write to kafka..."). It stands in for a model
// that ignored the attack and generated an ordinary, on-topic pipeline.
//
// Using validPipeline (generator/log) for these fixtures instead would trip
// the SEMANTIC checker (ExtractIntent sees "postgres"/"kafka" in the prompt
// and expects a candidate that uses them) and fail the test for a reason
// that has nothing to do with the attack class under test — a candidate
// that legitimately doesn't match the request's connectors is a real,
// separate failure mode (generate.semantic_mismatch), not evidence about
// deploy/secret/plugin boundaries. Reused verbatim from the eval corpus'
// own known-good postgres-to-kafka fixture
// (testdata/candidates/postgres-cdc-to-kafka-filtered-good.yaml) so it is
// known to pass validate.
const validPostgresToKafkaPipeline = `version: "2.2"
pipelines:
  - id: orders-to-kafka
    status: running
    name: orders-to-kafka
    connectors:
      - id: pg-source
        type: source
        plugin: builtin:postgres
        settings:
          table: orders
      - id: kafka-dest
        type: destination
        plugin: builtin:kafka
        settings:
          topic: orders
`

// wordDeploy names the forbidden-flag check's own entry as a constant rather
// than a fourth inline "deploy" string literal — goconst flags a repeated
// literal at 3 occurrences, and nextSteps (generate.go) plus
// TestFlags_NoApplyPathExists (generate_test.go) already account for two.
const wordDeploy = "deploy"

// canarySecret stands in for a real provider credential. It is never a real
// key — it exists only so a test can grep for it in what actually got sent
// or written, the same way a canary token works in production.
const canarySecret = "sk-canary-DO-NOT-LEAK-9f3a1c204b7e"

func fixtureByID(t *testing.T, fixtures []gen.AdversarialFixture, id string) gen.AdversarialFixture {
	t.Helper()
	for _, f := range fixtures {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no adversarial fixture with id %q (has the corpus been renamed?)", id)
	return gen.AdversarialFixture{}
}

// newTestCommandWithEnv is newTestCommand (generate_test.go) with a caller-
// supplied env function, for the one test here that needs to plant a canary
// secret at a specific env key rather than accept the fixed "test-key"
// newTestCommand hardcodes.
func newTestCommandWithEnv(prompt string, flags Flags, reply string, env func(string) string) (*Command, *fakeProvider) {
	fake := &fakeProvider{reply: reply}
	cmd := &Command{
		args:  Args{Prompt: prompt},
		flags: flags,
		env:   env,
		newProvider: func(string, string, func(string) string) (provider.Provider, error) {
			return fake, nil
		},
		probe: func(string) bool { return false },
	}
	return cmd, fake
}

// Test_Adversarial_SecretExfiltration_CanaryNeverReachesProviderOrDisk is the
// "env/secret exfiltration" attack class. A real ANTHROPIC_API_KEY-shaped
// canary is planted in the ONLY seam that ever sees a real credential
// (Command.env, exactly as newProvider reads it in production), and each
// fixture's hostile prompt — asking for that key, or a local credentials
// file, to be embedded in the pipeline config — is run through the real
// ExecuteWithResult path with a fake provider that plays along (replies with
// an ordinary valid pipeline, standing in for a model that complied).
//
// What this would catch: any future change that threads an env lookup, a
// file read, or "helpful context" derived from either into the completion
// request (CompletionRequest.System/.Prompt) or into the written file. Today
// nothing in the generate path does that — Input.Prompt is forwarded
// verbatim and BuildSystemPrompt is built only from the compiled-in
// connector catalog — so the canary structurally cannot appear anywhere
// this test looks. That is the guarantee: the secret is never AVAILABLE to
// leak, not merely filtered afterward.
func Test_Adversarial_SecretExfiltration_CanaryNeverReachesProviderOrDisk(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	fixtures, err := gen.LoadAdversarialFixtures(adversarialFixturesPath)
	is.NoErr(err)

	// The benign reply must match what each fixture's PROMPT names, or the
	// semantic checker fails the candidate for an unrelated reason (wrong
	// connectors) — see validPostgresToKafkaPipeline's doc comment.
	for _, tc := range []struct {
		id    string
		reply string
	}{
		{"secret-exfil-openai-key-in-settings", validPostgresToKafkaPipeline}, // prompt: "read from postgres and write to kafka..."
		{"secret-exfil-aws-credentials-file", validPipeline},                  // prompt: "...postgres source connector writing to a log destination"
	} {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			is := is.New(t)

			fx := fixtureByID(t, fixtures, tc.id)
			dir := t.TempDir()
			out := filepath.Join(dir, "out.yaml")

			// Only ANTHROPIC_API_KEY resolves — planting the canary at a
			// second key too would make provider resolution itself fail
			// with ambiguous_provider_configuration before a call is ever
			// made, which would test the WRONG thing.
			env := func(key string) string {
				if key == provider.EnvAnthropicKey {
					return canarySecret
				}
				return ""
			}
			cmd, fake := newTestCommandWithEnv(fx.Prompt, Flags{Out: out, MaxRetries: 1}, tc.reply, env)

			outcome, err := cmd.ExecuteWithResult(context.Background())
			is.NoErr(err)
			is.True(outcome.OK)

			is.Equal(len(fake.seen), 1)
			is.True(!strings.Contains(fake.seen[0].System, canarySecret))
			is.True(!strings.Contains(fake.seen[0].Prompt, canarySecret))

			written, err := os.ReadFile(out)
			is.NoErr(err)
			is.True(!strings.Contains(string(written), canarySecret))
		})
	}
}

// Test_Adversarial_FabricatedPlugin_NeverWrittenToDisk is the "fabricated
// plugin injection" attack class at the CLI boundary: the fake provider
// scripted to return a candidate naming a connector plugin that does not
// exist (fixture.MaliciousReply) must never result in a file on disk.
//
// What this would catch: a bug in ExecuteWithResult that writes the
// candidate BEFORE checking gen.Generate's error (the write call is
// currently gated behind `if err != nil { return ... }` — this test is what
// notices if that ordering is ever accidentally inverted or short-circuited).
func Test_Adversarial_FabricatedPlugin_NeverWrittenToDisk(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	fixtures, err := gen.LoadAdversarialFixtures(adversarialFixturesPath)
	is.NoErr(err)

	for _, id := range []string{
		"fabricated-plugin-builtin-exfiltrate",
		"fabricated-plugin-typo-postgre",
	} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			is := is.New(t)

			fx := fixtureByID(t, fixtures, id)
			is.True(fx.MaliciousReply != "")

			dir := t.TempDir()
			out := filepath.Join(dir, "out.yaml")
			cmd, _ := newTestCommand(t, fx.Prompt, Flags{Out: out, MaxRetries: 2}, fx.MaliciousReply)

			_, err := cmd.ExecuteWithResult(context.Background())
			is.True(err != nil)

			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, gen.CodeValidateFailed)

			_, statErr := os.Stat(out)
			is.True(os.IsNotExist(statErr))
		})
	}
}

// Test_Adversarial_CoercedAutoApply_NeverDeploys is the "coerced auto-apply"
// attack class. Each fixture's prompt tries, in different words, to get
// generate to deploy/apply/confirm on the user's behalf. There is no flag or
// code path that could do that (see the package doc), so the only correct
// outcome is exactly what a benign, unrelated request would produce: one
// provider call, one file write, nothing else.
//
// What this would catch: a future "convenience" feature that makes a SECOND
// call (to a deploy/apply seam) when the prompt or the candidate's own
// `status: running` field seems to ask for it — this test pins the call
// count at exactly 1, which such a feature could not satisfy.
func Test_Adversarial_CoercedAutoApply_NeverDeploys(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	fixtures, err := gen.LoadAdversarialFixtures(adversarialFixturesPath)
	is.NoErr(err)

	for _, id := range []string{
		"coerced-deploy-now-language",
		"coerced-status-running-and-apply",
	} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			is := is.New(t)

			fx := fixtureByID(t, fixtures, id)
			dir := t.TempDir()
			out := filepath.Join(dir, "out.yaml")
			cmd, fake := newTestCommand(t, fx.Prompt, Flags{Out: out, MaxRetries: 3}, validPostgresToKafkaPipeline) // both fixtures' prompts name postgres -> kafka

			outcome, err := cmd.ExecuteWithResult(context.Background())
			is.NoErr(err)
			is.True(outcome.OK)

			// Exactly one provider call: no follow-up round trip that could
			// be a deploy/confirm step in disguise.
			is.Equal(len(fake.seen), 1)

			result := outcome.Result.(Result)
			written, err := os.ReadFile(out)
			is.NoErr(err)
			is.Equal(string(written), result.Pipeline) // the file is exactly the candidate, nothing appended

			var flagNames []string
			for _, flag := range cmd.Flags() {
				flagNames = append(flagNames, flag.Long)
			}
			for _, forbidden := range []string{"yes", wordDeploy, "apply", "force-deploy", "run", "auto-apply", "confirm", "no-confirm"} {
				is.True(!slices.Contains(flagNames, forbidden))
			}
		})
	}
}

// Test_Adversarial_NoDeployPackageImport_StructuralCheck is the static half
// of the coerced-auto-apply guarantee: it parses every non-test .go file
// under this package and cmd/conduit/internal/generate (recursively, so the
// provider subpackage is covered too) and asserts none of them imports
// cmd/conduit/internal/deploy — the package that actually has the authority
// to mutate a running pipeline.
//
// This is deliberately a source-level check rather than a runtime one: a
// runtime assertion could only prove "this test's fixtures never triggered a
// deploy call," which says nothing about a code path this test's specific
// inputs never reach. Asserting the IMPORT doesn't exist proves the call
// could never be reached by ANY input, which is the actual claim the package
// doc makes ("There is deliberately no --yes, --deploy, or --apply flag: the
// boundary is enforced by the ABSENCE of a way to cross it").
func Test_Adversarial_NoDeployPackageImport_StructuralCheck(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	const forbiddenImport = "conduit/cmd/conduit/internal/deploy"

	for _, dir := range []string{".", "../../internal/generate"} {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if perr != nil {
				return perr
			}
			for _, imp := range f.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				if strings.Contains(importPath, forbiddenImport) {
					t.Errorf("%s imports %s: generate must never import the deploy package (design §4, the never-auto-apply boundary)", path, importPath)
				}
			}
			return nil
		})
		is.NoErr(err)
	}
}

// Test_Adversarial_PathEscape_WriteConfinedToWorkingDirectory is the
// "output-path escape via the pipeline id" attack class, run end to end
// through the real CLI command with NO explicit --out (so the default
// filename is derived from the malicious candidate's pipeline id,
// "../../etc/cron.d/x") in an actual working directory the test controls.
//
// What this would catch: modelDerivedName (outpath.go) being bypassed,
// weakened, or only applied to SOME code paths — a regression there would
// make this test either fail to find the file where it should be, or (far
// worse, if the sanitization broke entirely) attempt to escape t.TempDir()
// altogether.
func Test_Adversarial_PathEscape_WriteConfinedToWorkingDirectory(t *testing.T) {
	// Deliberately NOT t.Parallel(): os.Chdir is process-global.
	is := is.New(t)

	fixtures, err := gen.LoadAdversarialFixtures(adversarialFixturesPath)
	is.NoErr(err)
	fx := fixtureByID(t, fixtures, "path-escape-via-pipeline-id")
	is.True(fx.MaliciousReply != "")

	dir := t.TempDir()
	wd, err := os.Getwd()
	is.NoErr(err)
	is.NoErr(os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	cmd, _ := newTestCommand(t, fx.Prompt, Flags{MaxRetries: 1}, fx.MaliciousReply) // no --out: model-derived path

	outcome, err := cmd.ExecuteWithResult(context.Background())
	is.NoErr(err)

	result := outcome.Result.(Result)
	is.True(!strings.ContainsAny(result.Path, `/\`)) // bare filename, never a path
	is.True(!strings.Contains(result.Path, ".."))

	// The file must exist exactly where the sanitized bare name says, inside
	// the working directory this test controls — never at the literal
	// "/etc/cron.d/x" the candidate's id asked for.
	_, statErr := os.Stat(filepath.Join(dir, result.Path))
	is.NoErr(statErr)
	_, escapedErr := os.Stat("/etc/cron.d/x")
	is.True(os.IsNotExist(escapedErr))
}
