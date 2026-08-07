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

// Test_Adapters_HappyPath pins that each adapter returns the model's text and
// only a provider-REPORTED token count. A fabricated count would be
// indistinguishable from a real one in --json output users may bill against.
func Test_Adapters_HappyPath(t *testing.T) {
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

// Test_Adapters_HTTPErrorsAreProviderErrors pins the error code and that the
// message names the provider and status. A user seeing this needs to know
// which provider failed and why — not a stack trace from inside an HTTP client.
func Test_Adapters_HTTPErrorsAreProviderErrors(t *testing.T) {
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

// Test_Adapters_EmptyResponseIsAnError pins that a 200 with no text fails
// loudly. Returning an empty string would send the generation loop into a
// parse failure that blames the wrong layer.
func Test_Adapters_EmptyResponseIsAnError(t *testing.T) {
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

// Test_Adapters_RespectContextCancellation pins that a caller's cancellation
// aborts promptly rather than waiting out the per-call timeout.
func Test_Adapters_RespectContextCancellation(t *testing.T) {
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

// Test_Adapters_TimeoutIsBounded pins that a wedged provider fails rather than
// hanging an interactive command forever.
func Test_Adapters_TimeoutIsBounded(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	// Bounded for the same reason as in the cancellation test above.
	wedged := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(wedged.Close)

	start := time.Now()
	_, err := (&Ollama{Host: wedged.URL, Timeout: 200 * time.Millisecond}).
		Complete(context.Background(), CompletionRequest{Prompt: "x"})
	is.True(err != nil)
	codeIs(t, err, CodeProviderError)
	is.True(time.Since(start) < 5*time.Second)
}

// Test_Adapters_RequestModelOverridesAdapterDefault pins the precedence the
// --model flag depends on.
func Test_Adapters_RequestModelOverridesAdapterDefault(t *testing.T) {
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
