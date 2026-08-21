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

package provider

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeProviderError covers every way the provider call itself failed: network,
// timeout, rate limit, or an auth rejection from the provider. Unavailable
// (exit 3, environment) because the user's command was fine.
var CodeProviderError = conduiterr.Register("generate.provider_error", codes.Unavailable)

// DefaultTimeout bounds a single Complete call.
//
// Generation is interactive — a human is watching a spinner — so an unbounded
// wait on a wedged provider is worse than a clear failure they can retry. The
// retry loop applies this per ATTEMPT, not to the whole budget.
const DefaultTimeout = 60 * time.Second

// DefaultMaxTokens bounds the response.
//
// A generated pipeline config is small — around 350 tokens for a typical
// candidate in this package's own corpus — so the ceiling is not sized for
// the OUTPUT. It exists to absorb THINKING: on a model that runs extended or
// adaptive reasoning by default when the request omits an explicit thinking
// budget (e.g. Anthropic's Sonnet 5, DefaultAnthropicModel), max_tokens caps
// thinking output and response text TOGETHER, sharing one budget. A ceiling
// sized only for the ~350-token config truncates the model mid-think,
// mid-YAML — and extractCandidate deliberately tolerates an unterminated
// fence and hands the partial config to the validator, so the truncation
// never surfaces as "the model ran out of budget": it looks like an ordinary
// validation failure, burns a retry, and (in an eval transcript) reads as
// model error. 16384 is sized to comfortably absorb a normal think for a
// request this small while still bounding a genuinely wedged/looping model.
const DefaultMaxTokens = 16384

// Doer is the HTTP seam, matching the two-method shape the `ollama` built-in
// processor already uses (pkg/plugin/processor/builtin/impl/ollama). Copying
// that shape rather than inventing a second one keeps the tree to a single
// hand-rolled-HTTP-client pattern.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// providerErrorf builds a CodeProviderError naming the provider.
//
// The message never carries a stack trace or a raw dump: this is rendered to a
// user who needs to know which provider failed and why, and a stack trace from
// inside an HTTP client tells them nothing actionable.
func providerErrorf(name string, format string, args ...any) error {
	e := conduiterr.New(CodeProviderError, fmt.Sprintf("%s: %s", name, fmt.Sprintf(format, args...)))
	e.Suggestion = fmt.Sprintf("check the %s provider's credentials and connectivity, or select another with --provider", name)
	return e
}

// readErrorBody extracts a bounded, single-line excerpt of an error response.
//
// Bounded because a provider returning an HTML error page would otherwise dump
// it into a terminal; single-line because this lands inside an error message.
// The status code is the load-bearing part — the body is a hint.
func readErrorBody(r io.Reader) string {
	if r == nil {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(r, 512))
	if err != nil || len(b) == 0 {
		return ""
	}
	return strings.Join(strings.Fields(string(b)), " ")
}

// checkStatus converts a non-2xx response into a provider error.
func checkStatus(name string, resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if body := readErrorBody(resp.Body); body != "" {
		return providerErrorf(name, "HTTP %d: %s", resp.StatusCode, body)
	}
	return providerErrorf(name, "HTTP %d", resp.StatusCode)
}
