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
	"net/http/httptest"
	"testing"
	"time"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// capturing wraps an httptest server and records the last request body and
// headers, so tests assert what was actually sent on the wire rather than
// trusting the struct tags.
type capturing struct {
	*httptest.Server
	body    []byte
	headers http.Header
	path    string
}

func newCapturing(t *testing.T, status int, respBody string) *capturing {
	t.Helper()
	c := &capturing{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.body, _ = io.ReadAll(r.Body)
		c.headers = r.Header.Clone()
		c.path = r.URL.Path
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(c.Close)
	return c
}

func codeIs(t *testing.T, err error, want conduiterr.Code) {
	t.Helper()
	ce, ok := conduiterr.Get(err)
	if !ok {
		t.Fatalf("not a ConduitError: %v", err)
	}
	if ce.Code != want {
		t.Fatalf("code = %v, want %v", ce.Code.Reason(), want.Reason())
	}
}

const (
	anthropicOK = `{"content":[{"type":"text","text":"version: 2.2"}],"usage":{"input_tokens":10,"output_tokens":5}}`
	ollamaOK    = `{"response":"version: 2.2","prompt_eval_count":10,"eval_count":5}`
	openAIOK    = `{"choices":[{"message":{"role":"assistant","content":"version: 2.2"}}],"usage":{"total_tokens":15}}`
)

// TestAdapters_HappyPath pins that each adapter returns the model's text and
// only a provider-REPORTED token count. A fabricated count would be
// indistinguishable from a real one in --json output users may bill against.
func TestAdapters_HappyPath(t *testing.T) {
	t.Parallel()

	t.Run("anthropic", func(t *testing.T) {
		is := is.New(t)
		srv := newCapturing(t, 200, anthropicOK)
		p := &Anthropic{APIKey: "sk-ant", BaseURL: srv.URL}

		got, err := p.Complete(context.Background(), CompletionRequest{System: "sys", Prompt: "ask"})
		is.NoErr(err)
		is.Equal(got.Text, "version: 2.2")
		is.Equal(got.TokensUsed, 15)
		is.Equal(srv.path, "/v1/messages")

		// The version header is mandatory; without it the API rejects the call.
		is.Equal(srv.headers.Get("anthropic-version"), anthropicVersion)
		is.Equal(srv.headers.Get("x-api-key"), "sk-ant")

		var sent anthropicRequest
		is.NoErr(json.Unmarshal(srv.body, &sent))
		is.Equal(sent.System, "sys")
		is.Equal(sent.Messages[0].Content, "ask") // prompt sent verbatim
		is.Equal(sent.Model, DefaultAnthropicModel)
	})

	t.Run("ollama", func(t *testing.T) {
		is := is.New(t)
		srv := newCapturing(t, 200, ollamaOK)
		p := &Ollama{Host: srv.URL}

		got, err := p.Complete(context.Background(), CompletionRequest{System: "sys", Prompt: "ask"})
		is.NoErr(err)
		is.Equal(got.Text, "version: 2.2")
		is.Equal(got.TokensUsed, 15)
		is.Equal(srv.path, "/api/generate")

		var sent ollamaRequest
		is.NoErr(json.Unmarshal(srv.body, &sent))
		is.Equal(sent.Prompt, "ask")
		is.True(!sent.Stream) // single-shot; a streaming body would not decode
	})

	t.Run("openai", func(t *testing.T) {
		is := is.New(t)
		srv := newCapturing(t, 200, openAIOK)
		p := &OpenAI{APIKey: "sk-oai", BaseURL: srv.URL}

		got, err := p.Complete(context.Background(), CompletionRequest{System: "sys", Prompt: "ask"})
		is.NoErr(err)
		is.Equal(got.Text, "version: 2.2")
		is.Equal(got.TokensUsed, 15)
	})
}

// TestAdapters_HTTPErrorsAreProviderErrors pins the error code and that the
// message names the provider and status. A user seeing this needs to know
// which provider failed and why — not a stack trace from inside an HTTP client.
func TestAdapters_HTTPErrorsAreProviderErrors(t *testing.T) {
	t.Parallel()

	for _, status := range []int{401, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			is := is.New(t)

			for name, build := range map[string]func(url string) Provider{
				"anthropic": func(u string) Provider { return &Anthropic{BaseURL: u} },
				"ollama":    func(u string) Provider { return &Ollama{Host: u} },
				"openai":    func(u string) Provider { return &OpenAI{BaseURL: u} },
			} {
				srv := newCapturing(t, status, `{"error":"nope"}`)
				_, err := build(srv.URL).Complete(context.Background(), CompletionRequest{Prompt: "x"})
				is.True(err != nil)
				codeIs(t, err, CodeProviderError)
				is.True(contains(err.Error(), name)) // names which provider

				// The STATUS must appear. Asserting only the code is not
				// enough: with status checking removed, an error body decodes
				// to empty content and still yields a provider_error, so a
				// code-only assertion passes for the wrong reason and the user
				// is told "empty response" when the truth is HTTP 401.
				is.True(contains(err.Error(), fmt.Sprint(status)))
			}
		})
	}
}

// TestAdapters_EmptyResponseIsAnError pins that a 200 with no text fails
// loudly. Returning an empty string would send the generation loop into a
// parse failure that blames the wrong layer.
func TestAdapters_EmptyResponseIsAnError(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		body  string
		build func(url string) Provider
	}{
		"anthropic no blocks": {`{"content":[],"usage":{}}`, func(u string) Provider { return &Anthropic{BaseURL: u} }},
		"anthropic blank":     {`{"content":[{"type":"text","text":"  "}]}`, func(u string) Provider { return &Anthropic{BaseURL: u} }},
		"ollama blank":        {`{"response":""}`, func(u string) Provider { return &Ollama{Host: u} }},
		"openai no choices":   {`{"choices":[]}`, func(u string) Provider { return &OpenAI{BaseURL: u} }},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			srv := newCapturing(t, 200, tc.body)
			_, err := tc.build(srv.URL).Complete(context.Background(), CompletionRequest{Prompt: "x"})
			is.True(err != nil)
			codeIs(t, err, CodeProviderError)
			is.True(contains(err.Error(), "empty response"))
		})
	}
}

// TestAdapters_EmptyResponseIsMarkedUnusable_AndCarriesTokens is the
// regression test for the finding that a refusal (a 2xx with an empty
// completion) was reported identically to absence of data (a 429, a
// timeout) and its tokens were silently dropped from cost accounting: the
// response DID decode, so the provider-reported usage is known even though
// the completion is unusable, and that must survive on CompletionResult
// even though Complete returns an error — a caller that only inspects the
// error return (as code used to) would otherwise undercount real spend.
func TestAdapters_EmptyResponseIsMarkedUnusable_AndCarriesTokens(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		body       string
		build      func(url string) Provider
		wantTokens int
	}{
		"anthropic": {
			`{"content":[],"usage":{"input_tokens":7,"output_tokens":3}}`,
			func(u string) Provider { return &Anthropic{BaseURL: u} }, 10,
		},
		"ollama": {
			`{"response":"","prompt_eval_count":7,"eval_count":3}`,
			func(u string) Provider { return &Ollama{Host: u} }, 10,
		},
		"openai": {
			`{"choices":[],"usage":{"total_tokens":10}}`,
			func(u string) Provider { return &OpenAI{BaseURL: u} }, 10,
		},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			srv := newCapturing(t, 200, tc.body)
			got, err := tc.build(srv.URL).Complete(context.Background(), CompletionRequest{Prompt: "x"})
			is.True(err != nil)
			is.True(IsUnusableResponse(err)) // a refusal, not absence of data
			is.Equal(got.TokensUsed, tc.wantTokens)
			_, hasStatus := HTTPStatus(err)
			is.True(!hasStatus) // a 2xx has no failing status to report
		})
	}
}

// TestAdapters_HTTPErrorsCarryHTTPStatus pins the structured half of a
// checkStatus failure: the numeric status is recoverable via HTTPStatus
// without parsing message text, and such an error is never marked
// IsUnusableResponse (a non-2xx means no usable response was obtained at
// all, a different failure mode than a refusal).
//
// openai is deliberately excluded: it wraps the vendored goopenai SDK
// client rather than calling checkStatus directly (see openai.go's own doc
// comment on why), so an HTTP error from it never carries a *httpStatusError
// today — a real gap, but a pre-existing one this fix does not widen, and
// out of scope here since the live capture harness (transcript_capture_test.go)
// only ever drives the anthropic adapter.
func TestAdapters_HTTPErrorsCarryHTTPStatus(t *testing.T) {
	t.Parallel()

	for name, build := range map[string]func(url string) Provider{
		"anthropic": func(u string) Provider { return &Anthropic{BaseURL: u} },
		"ollama":    func(u string) Provider { return &Ollama{Host: u} },
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			srv := newCapturing(t, 429, `{"error":"nope"}`)
			_, err := build(srv.URL).Complete(context.Background(), CompletionRequest{Prompt: "x"})
			is.True(err != nil)
			status, ok := HTTPStatus(err)
			is.True(ok)
			is.Equal(status, 429)
			is.True(!IsUnusableResponse(err))
		})
	}
}

// Test_Anthropic_ConcatenatesTextBlocks pins that a response split across
// blocks is joined. Taking only the first block would silently truncate the
// generated config — which would then fail validation for the wrong reason.
func Test_Anthropic_ConcatenatesTextBlocks(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	srv := newCapturing(t, 200,
		`{"content":[{"type":"text","text":"version: "},{"type":"thinking","text":"IGNORED"},{"type":"text","text":"2.2"}]}`)
	got, err := (&Anthropic{BaseURL: srv.URL}).Complete(context.Background(), CompletionRequest{Prompt: "x"})
	is.NoErr(err)
	is.Equal(got.Text, "version: 2.2") // joined, and non-text blocks skipped
}

// TestAdapters_RespectContextCancellation pins that a caller's cancellation
// aborts promptly rather than waiting out the per-call timeout.
func TestAdapters_RespectContextCancellation(t *testing.T) {
	t.Parallel()

	// The wait is BOUNDED. httptest.Server.Close waits for in-flight handlers,
	// and a handler that blocks purely on r.Context().Done() deadlocks against
	// it — the cleanup waits for the handler, the handler waits for a
	// cancellation the cleanup would have caused.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)

	for name, build := range map[string]func(url string) Provider{
		"anthropic": func(u string) Provider { return &Anthropic{BaseURL: u} },
		"ollama":    func(u string) Provider { return &Ollama{Host: u} },
		"openai":    func(u string) Provider { return &OpenAI{BaseURL: u} },
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			done := make(chan error, 1)
			go func() {
				_, err := build(slow.URL).Complete(ctx, CompletionRequest{Prompt: "x"})
				done <- err
			}()

			select {
			case err := <-done:
				is.True(err != nil)
			case <-time.After(5 * time.Second):
				t.Fatal("Complete ignored a cancelled context")
			}
		})
	}
}

// TestAdapters_TimeoutIsBounded pins that a wedged provider fails rather than
// hanging an interactive command forever, and (round-2 review of #2814,
// "safeFailureReason collapses DNS failure, connection-refused, timeout...
// to one string") that the failure is specifically identifiable via
// IsTimeout — distinguishing "our own deadline expired" from every OTHER
// transport-level miss (DNS failure, connection refused), which HTTPStatus
// alone can't tell apart (none of those ever got a response with a status
// to report).
func TestAdapters_TimeoutIsBounded(t *testing.T) {
	t.Parallel()

	// Bounded for the same reason as in the cancellation test above.
	wedged := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(wedged.Close)

	for name, build := range map[string]func(url string) Provider{
		"anthropic": func(u string) Provider { return &Anthropic{BaseURL: u, Timeout: 200 * time.Millisecond} },
		"ollama":    func(u string) Provider { return &Ollama{Host: u, Timeout: 200 * time.Millisecond} },
		"openai":    func(u string) Provider { return &OpenAI{BaseURL: u, Timeout: 200 * time.Millisecond} },
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			start := time.Now()
			_, err := build(wedged.URL).Complete(context.Background(), CompletionRequest{Prompt: "x"})
			is.True(err != nil)
			codeIs(t, err, CodeProviderError)
			is.True(time.Since(start) < 5*time.Second)
			is.True(IsTimeout(err))
			// Never confused with the OTHER thing HTTPStatus/IsUnusableResponse
			// report false for: no response ever arrived, so neither applies.
			_, hasStatus := HTTPStatus(err)
			is.True(!hasStatus)
			is.True(!IsUnusableResponse(err))
		})
	}
}

// TestAdapters_NonTimeoutTransportErrorIsNotMarkedAsTimeout is the
// negative case for IsTimeout: a connection actively refused (not a
// deadline expiring) must never be misreported as a timeout — the two call
// for different remediation (retry later vs. check the endpoint/network).
func TestAdapters_NonTimeoutTransportErrorIsNotMarkedAsTimeout(t *testing.T) {
	t.Parallel()

	// A server that accepts and immediately closes the connection is a
	// reliable, fast way to force "connection refused"/"EOF" rather than a
	// deadline — no sleeping, no wedged handler.
	//
	// N5 (round-3 review of #2814): this handler runs on net/http's OWN
	// per-request goroutine, never the test goroutine — calling
	// t.Fatal/t.Fatalf here was an invalid use of *testing.T (its docs:
	// "FailNow must be called from the goroutine running the test... function,
	// not from other goroutines"). Neither branch below is expected to
	// ever actually fire against httptest's server transport (its
	// ResponseWriter always implements Hijacker, and Hijack on a fresh
	// connection does not fail), so panicking is the correct "this should
	// never happen" signal here: net/http recovers a per-connection panic
	// and logs it (visible with `go test -v`) without the goroutine-safety
	// hazard a *testing.T call would carry.
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			panic("test setup: ResponseWriter does not support hijacking")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			panic(fmt.Sprintf("test setup: hijack: %v", err))
		}
		conn.Close()
	}))
	t.Cleanup(refusing.Close)

	for name, build := range map[string]func(url string) Provider{
		"anthropic": func(u string) Provider { return &Anthropic{BaseURL: u} },
		"ollama":    func(u string) Provider { return &Ollama{Host: u} },
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			_, err := build(refusing.URL).Complete(context.Background(), CompletionRequest{Prompt: "x"})
			is.True(err != nil)
			is.True(!IsTimeout(err))
		})
	}
}

// TestAdapters_RequestModelOverridesAdapterDefault pins the precedence the
// --model flag depends on.
func TestAdapters_RequestModelOverridesAdapterDefault(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	srv := newCapturing(t, 200, ollamaOK)
	_, err := (&Ollama{Host: srv.URL, Model: "adapter-default"}).
		Complete(context.Background(), CompletionRequest{Prompt: "x", Model: "from-request"})
	is.NoErr(err)

	var sent ollamaRequest
	is.NoErr(json.Unmarshal(srv.body, &sent))
	is.Equal(sent.Model, "from-request")
}

// Test_DefaultMaxTokens_AbsorbsThinkingNotJustOutput pins the actual value,
// not just that SOME constant is wired through (TestAdapters_
// MaxTokensPassedThrough below covers wiring). A generated pipeline config
// is small — around 350 tokens for a typical candidate in this package's own
// corpus — so 4096 looks generous for the OUTPUT alone; it is not generous
// for output PLUS an adaptive/extended think a model can run by default when
// the request sets no explicit thinking budget (Anthropic's Sonnet 5,
// DefaultAnthropicModel, shares one max_tokens budget across both). This
// test fails if the ceiling regresses back toward a value sized for the
// config text alone.
func Test_DefaultMaxTokens_AbsorbsThinkingNotJustOutput(t *testing.T) {
	is := is.New(t)
	is.Equal(DefaultMaxTokens, 16384)
}

// TestAdapters_MaxTokensPassedThrough pins that DefaultMaxTokens actually
// reaches the request body, for every adapter that has a max-tokens field to
// set. On a model that runs adaptive/extended thinking by default when the
// request doesn't set an explicit thinking budget (e.g. Anthropic's Sonnet 5,
// DefaultAnthropicModel — see DefaultMaxTokens' doc comment), max_tokens caps
// thinking AND response text together: a ceiling too small for that silently
// truncates the generated YAML mid-document rather than failing loudly, which
// is exactly the invisible-truncation bug this constant's value guards
// against. A regression that drops the field, hardcodes a smaller value, or
// forgets to wire DefaultMaxTokens into a new adapter's request must fail
// this test.
func TestAdapters_MaxTokensPassedThrough(t *testing.T) {
	t.Parallel()

	t.Run("anthropic", func(t *testing.T) {
		is := is.New(t)
		srv := newCapturing(t, 200, anthropicOK)
		_, err := (&Anthropic{BaseURL: srv.URL}).Complete(context.Background(), CompletionRequest{Prompt: "x"})
		is.NoErr(err)

		var sent anthropicRequest
		is.NoErr(json.Unmarshal(srv.body, &sent))
		is.Equal(sent.MaxTokens, DefaultMaxTokens)
	})

	t.Run("openai", func(t *testing.T) {
		is := is.New(t)
		srv := newCapturing(t, 200, openAIOK)
		_, err := (&OpenAI{BaseURL: srv.URL}).Complete(context.Background(), CompletionRequest{Prompt: "x"})
		is.NoErr(err)

		// Decode just the field this test cares about rather than importing
		// go-openai's own request struct here — this test asserts what's on
		// the wire, not the SDK's shape.
		var sent struct {
			MaxTokens int `json:"max_tokens"`
		}
		is.NoErr(json.Unmarshal(srv.body, &sent))
		is.Equal(sent.MaxTokens, DefaultMaxTokens)
	})
}

// Test_Reachable pins the auto-detection probe, including that it does NOT
// require a loaded model: a reachable server with no model is still a
// candidate, and the completion call reports the missing model with a message
// that says so.
func Test_Reachable(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	up := newCapturing(t, 200, "{}")
	is.True(Reachable(up.URL, nil, time.Second))

	// 404 at the root still means something is listening and speaking HTTP.
	notFound := newCapturing(t, 404, "")
	is.True(Reachable(notFound.URL, nil, time.Second))

	// A 5xx means it is not usable.
	broken := newCapturing(t, 503, "")
	is.True(!Reachable(broken.URL, nil, time.Second))

	// Nothing listening.
	is.True(!Reachable("http://127.0.0.1:1", nil, 500*time.Millisecond))
}
