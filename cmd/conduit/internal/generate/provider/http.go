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
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
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

// errUnusableResponse marks a provider error that occurred AFTER an HTTP
// response was already in hand — a body that doesn't decode, or a
// well-formed but empty completion (a refusal) — as distinct from a
// transport-level failure (network error, timeout, non-2xx status) where no
// usable response ever arrived. The distinction matters: a refusal is a
// BILLED, attempted call and must never be reported the same way as
// absence of data the way an unreachable endpoint or a rate limit is (see
// IsUnusableResponse and RequestOutcome.Unusable,
// transcript_capture_test.go / transcript.go).
var errUnusableResponse = cerrors.New("provider response could not be used")

// IsUnusableResponse reports whether err was produced by a call that DID
// receive an HTTP response but could not turn it into a usable completion —
// see errUnusableResponse. False for every other provider error, including
// one produced by checkStatus (a non-2xx status means no usable response
// was ever obtained in the first place, which is a different failure mode
// entirely).
func IsUnusableResponse(err error) bool {
	return cerrors.Is(err, errUnusableResponse)
}

// unusableResponseError wraps err to mark it via IsUnusableResponse,
// transparently: Error() and Unwrap() forward to cause unchanged, so
// marking never changes what a user or a log line sees.
type unusableResponseError struct {
	cause error
}

func (e *unusableResponseError) Error() string        { return e.cause.Error() }
func (e *unusableResponseError) Unwrap() error        { return e.cause }
func (e *unusableResponseError) Is(target error) bool { return target == errUnusableResponse }

// markUnusableResponse wraps err (nil-safe) so IsUnusableResponse(err)
// reports true. Use only for a failure detected strictly AFTER a 2xx
// response was already parsed — never for a transport or status failure.
func markUnusableResponse(err error) error {
	if err == nil {
		return nil
	}
	return &unusableResponseError{cause: err}
}

// checkStatus converts a non-2xx response into a provider error.
//
// The returned error is wrapped in *httpStatusError so a caller can recover
// the numeric status via HTTPStatus without parsing message text — see that
// function's doc comment for why this exists.
func checkStatus(name string, resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	var err error
	if body := readErrorBody(resp.Body); body != "" {
		err = providerErrorf(name, "HTTP %d: %s", resp.StatusCode, body)
	} else {
		err = providerErrorf(name, "HTTP %d", resp.StatusCode)
	}
	return &httpStatusError{cause: err, statusCode: resp.StatusCode}
}

// httpStatusError carries checkStatus's numeric HTTP status alongside the
// provider error it wraps, transparently: Error() and Unwrap() forward to
// cause unchanged, so wrapping never changes what a user or a log line
// sees — only what a caller doing HTTPStatus(err) can recover.
type httpStatusError struct {
	cause      error
	statusCode int
}

func (e *httpStatusError) Error() string { return e.cause.Error() }
func (e *httpStatusError) Unwrap() error { return e.cause }

// HTTPStatus extracts the response status code from err, when err (or
// something it wraps) came from checkStatus. ok is false for every other
// provider error — a network failure, a decode failure, an empty response —
// because none of those had a response WITH a status code worth reporting,
// either because no response ever arrived (network error, timeout) or
// because it arrived with a 2xx and failed for a reason checkStatus never
// saw.
//
// This is the structured half of the fix for storing a provider error's raw
// text in a committed file (transcript_capture_test.go's
// safeFailureReason): the status code is the load-bearing part of a
// checkStatus failure (invariant documented on readErrorBody above) and is
// safe to persist verbatim, unlike the response body readErrorBody embeds
// into the human-readable message, which may echo back caller-supplied or
// provider-controlled content (e.g. a 401 body quoting the rejected key).
func HTTPStatus(err error) (status int, ok bool) {
	var se *httpStatusError
	if cerrors.As(err, &se) {
		return se.statusCode, true
	}
	return 0, false
}

// errTimeout marks a provider error whose underlying transport failure was
// specifically the calling context's deadline expiring (context.
// DeadlineExceeded) — as opposed to every OTHER transport-level miss
// HTTPStatus also returns ok=false for: DNS resolution failure, connection
// refused, a TLS handshake failure, and so on. Nit from the round-2 review
// of #2814: without this, safeFailureReason (transcript_capture_test.go)
// collapses all of those to the identical string (CodeProviderError's
// generic Reason(), no HTTP status to append — none of them ever got a
// response), which is a real triage regression: "our own deadline expired"
// (captureWallClockBudget, DefaultTimeout) and "the provider's DNS is
// broken" call for different remediation and read identically in a
// committed manifest/tombstone today.
var errTimeout = cerrors.New("provider call timed out")

// IsTimeout reports whether err was produced by a Do() call that failed
// because ctx's deadline expired — see errTimeout. False for every other
// provider error, including a non-timeout network failure.
func IsTimeout(err error) bool {
	return cerrors.Is(err, errTimeout)
}

// timeoutError wraps err to mark it via IsTimeout, transparently: Error()
// and Unwrap() forward to cause unchanged, so marking never changes what a
// user or a log line sees.
type timeoutError struct {
	cause error
}

func (e *timeoutError) Error() string        { return e.cause.Error() }
func (e *timeoutError) Unwrap() error        { return e.cause }
func (e *timeoutError) Is(target error) bool { return target == errTimeout }

// MarkIfTimeout wraps err (nil-safe, and a no-op if err is already nil or
// transportErr does not satisfy context.DeadlineExceeded) so IsTimeout(err)
// reports true — call at the Do() call site, passing the ORIGINAL transport
// error (transportErr) to test, since providerErrorf's %v-formatted result
// no longer carries a traversable Unwrap chain back to it.
func MarkIfTimeout(wrapped error, transportErr error) error {
	if wrapped == nil || !cerrors.Is(transportErr, context.DeadlineExceeded) {
		return wrapped
	}
	return &timeoutError{cause: wrapped}
}
