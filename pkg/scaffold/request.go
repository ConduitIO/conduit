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

package scaffold

import (
	"fmt"
	"os"
	"regexp"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/scaffold/template"
)

// Kind and its values are re-exported from package template so callers of
// this package never need to import the template subpackage directly.
type Kind = template.Kind

const (
	KindConnector = template.KindConnector
	KindProcessor = template.KindProcessor
)

// Language selects the target language for the scaffold. Go is the only
// value that produces a scaffold today — see doc.go and the design doc's
// feasibility verdict for why Python is a gated error rather than a
// half-implemented code path.
type Language string

// LanguageGo is the only supported Language.
const LanguageGo Language = "go"

// Request is the input to Generate.
type Request struct {
	// Kind selects connector or processor.
	Kind Kind
	// Language is the target language flag (--lang). Only LanguageGo
	// produces a scaffold; any other non-empty value is a gated error (see
	// validate).
	Language Language
	// Name is the connector/processor's resource name (the positional CLI
	// arg, e.g. "s3" for conduit-connector-s3). It becomes the Go package
	// name of the scaffolded module, so it must be a valid Go identifier —
	// see validate.
	Name string
	// Module is the Go module path (--module). If empty, validate requires
	// it (see Request's doc on TTY prompting being the CLI layer's job, not
	// this package's). When set, it must end in
	// "conduit-connector-<Name>"/"conduit-processor-<Name>" (matching Name),
	// per the templates' own setup.sh contract.
	Module string
	// Path is the destination directory (--path). Defaults to
	// "./conduit-<kind>-<name>" in the current directory when empty.
	Path string
	// SDKVersion optionally overrides the connector/processor SDK version
	// pinned in the embedded snapshot (--sdk-version). Empty means "use the
	// snapshot's pinned version" (see template.Kind.SDKVersion).
	SDKVersion string
	// Git controls whether GitInit runs (--git/--no-git). Defaults to true.
	Git bool
	// SkipGenerate skips InstallTool+Generate (--skip-generate) — both
	// templates ship pre-generated output, so the result still builds; this
	// is the escape hatch when there's no network to install the generator
	// tool.
	SkipGenerate bool
	// Force allows overwriting an existing destination directory (--force).
	Force bool
}

// Step names, i.e. the values Generate uses for StepResult.Name. Exported so
// a caller rendering per-step human output (see cmd/conduit/internal/scaffoldcmd)
// or asserting on --json output in a test switches on these instead of
// duplicating the literal strings.
const (
	StepToolchain       = "toolchain"
	StepExtractTemplate = "extract_template"
	StepRewriteModule   = "rewrite_module"
	StepSDKVersion      = "sdk_version"
	StepInstallTools    = "install_tools"
	StepGenerate        = "generate"
	StepBuild           = "build"
	StepGitInit         = "git_init"
)

// StepResult is one pipeline step's outcome, part of the --json envelope's
// result.steps[] (per the CLI output conventions and this command's design
// doc).
type StepResult struct {
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"durationMs"`
	// Message carries a short human-readable detail — set on failure (e.g. a
	// step that GitInit's best-effort handling downgraded instead of
	// failing the whole run), empty on an ordinary pass.
	Message string `json:"message,omitempty"`
}

// Result is Generate's successful return value.
type Result struct {
	Kind        Kind         `json:"kind"`
	Language    Language     `json:"language"`
	Name        string       `json:"name"`
	Module      string       `json:"module"`
	Path        string       `json:"path"`
	TemplateRef string       `json:"templateRef"`
	SDKVersion  string       `json:"sdkVersion"`
	Steps       []StepResult `json:"steps"`
	ElapsedMS   int64        `json:"elapsedMs"`
	NextSteps   []string     `json:"nextSteps"`
}

// namePattern enforces that Name is usable as a Go package name: the
// templates' setup.sh substitutes Name verbatim into `package
// connectorname`/`package processorname`, so a Name containing characters
// invalid in a Go identifier (a hyphen, most commonly — "google-pubsub"
// would produce "package google-pubsub", a syntax error) would silently
// pass rewrite and only fail much later, opaquely, at the verified `go
// build` step. Rejecting it up front at validate time, with a message that
// says exactly what's wrong, is strictly better UX for the same underlying
// constraint.
var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// modulePrefix returns the required "conduit-connector-"/"conduit-processor-"
// module suffix prefix for kind.
func modulePrefix(kind Kind) string {
	if kind == KindProcessor {
		return "conduit-processor-"
	}
	return "conduit-connector-"
}

// validate checks req and fills in defaults (Path, Git), returning the
// resolved Request or a *conduiterr.ConduitError classifying what's wrong.
// It does not touch the filesystem beyond checking whether Path already
// exists (Request.Force) — no directory is created here.
func validate(req Request) (Request, error) {
	if req.Language != LanguageGo {
		return req, newErr(CodeUnsupportedLanguage, unsupportedLanguageMessage(req.Language),
			"only --lang go is available today; see docs/design-documents/20260707-python-connector-sdk.md "+
				"for the blocker on Python and track it there before filing a duplicate issue",
		)
	}

	if req.Name == "" {
		return req, newErr(CodeInvalidModule, "a connector/processor name is required",
			fmt.Sprintf("conduit %s new <name>, e.g. conduit %s new s3", string(req.Kind), string(req.Kind)),
		)
	}
	if !namePattern.MatchString(req.Name) {
		return req, newErr(CodeInvalidModule, fmt.Sprintf("name %q is not a valid Go identifier", req.Name),
			"use letters, digits, and underscores only (it becomes the scaffolded Go package name), e.g. \"s3\" not \"google-cloud-storage\"",
		)
	}

	if req.Module == "" {
		return req, newErr(CodeInvalidModule, "--module is required",
			fmt.Sprintf("--module github.com/<you>/%s%s", modulePrefix(req.Kind), req.Name),
		)
	}
	wantSuffix := modulePrefix(req.Kind) + req.Name
	if !moduleMatches(req.Module, wantSuffix) {
		return req, newErr(CodeInvalidModule,
			fmt.Sprintf("--module %q must match \"github.com/<owner>/%s\" for name %q", req.Module, wantSuffix, req.Name),
			fmt.Sprintf("--module github.com/<you>/%s", wantSuffix),
		)
	}

	if req.Path == "" {
		req.Path = "./" + modulePrefix(req.Kind) + req.Name
	}
	if !req.Force {
		if _, err := os.Stat(req.Path); err == nil {
			return req, newErr(CodeDestinationExists, fmt.Sprintf("destination %q already exists", req.Path),
				"pass --force to overwrite it, or choose a different --path",
			)
		}
	}

	return req, nil
}

// newErr builds a *conduiterr.ConduitError with a suggestion set, since
// every validate failure has a concrete, actionable fix (per the CLI output
// conventions: scaffolding's error MUST carry suggestion, not a bare
// {code,message}).
func newErr(code conduiterr.Code, message, suggestion string) error {
	ce := conduiterr.New(code, message)
	ce.Suggestion = suggestion
	return ce
}

// moduleMatches reports whether module is "github.com/<owner>/<suffix>" for
// a single path-segment owner (setup.sh's own regex,
// `^github.com\/.*\/conduit-connector-(.*)$`, is looser — it allows a
// multi-segment owner via `.*` — but every real GitHub module path has
// exactly one owner segment, and being stricter here catches a
// copy-pasted-wrong module before it reaches git/go tooling instead of
// producing a confusing downstream failure).
var moduleRe = regexp.MustCompile(`^github\.com/[^/]+/(.+)$`)

func moduleMatches(module, wantSuffix string) bool {
	m := moduleRe.FindStringSubmatch(module)
	return m != nil && m[1] == wantSuffix
}

func unsupportedLanguageMessage(lang Language) string {
	if lang == "" {
		return "--lang is required"
	}
	return fmt.Sprintf("--lang %q is not supported yet", string(lang))
}
