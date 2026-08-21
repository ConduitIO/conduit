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
	"net/http"
	"strings"
	"time"

	goopenai "github.com/sashabaranov/go-openai"
)

// DefaultOpenAIModel is the recommended default.
const DefaultOpenAIModel = goopenai.GPT4o

// OpenAI is a single-turn adapter over the chat completions API.
//
// It wraps the ALREADY-VENDORED github.com/sashabaranov/go-openai — the same
// client the `openai` built-in processor uses. Hand-rolling a second OpenAI
// HTTP client would be the YAGNI violation here, not avoiding one: the
// dependency is already paid for and already maintained by someone else.
type OpenAI struct {
	APIKey  string
	Model   string
	BaseURL string
	Timeout time.Duration
	// HTTP overrides the client's transport. Only set by tests, which point it
	// at an httptest server via BaseURL.
	HTTP *http.Client
}

func (o *OpenAI) Name() string { return NameOpenAI }

func (o *OpenAI) Complete(ctx context.Context, req CompletionRequest) (CompletionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, orDefault(o.Timeout, DefaultTimeout))
	defer cancel()

	cfg := goopenai.DefaultConfig(o.APIKey)
	if o.BaseURL != "" {
		cfg.BaseURL = strings.TrimSuffix(o.BaseURL, "/")
	}
	if o.HTTP != nil {
		cfg.HTTPClient = o.HTTP
	}

	resp, err := goopenai.NewClientWithConfig(cfg).CreateChatCompletion(ctx, goopenai.ChatCompletionRequest{
		Model:     orDefaultStr(req.Model, orDefaultStr(o.Model, DefaultOpenAIModel)),
		MaxTokens: DefaultMaxTokens,
		Messages: []goopenai.ChatCompletionMessage{
			{Role: goopenai.ChatMessageRoleSystem, Content: req.System},
			{Role: goopenai.ChatMessageRoleUser, Content: req.Prompt},
		},
	})
	if err != nil {
		// MarkIfTimeout distinguishes "ctx's deadline expired" from every
		// OTHER transport-level miss — see errTimeout's doc comment,
		// provider/http.go. A no-op if the go-openai SDK's error doesn't
		// unwrap to context.DeadlineExceeded.
		return CompletionResult{}, MarkIfTimeout(providerErrorf(o.Name(), "%v", err), err)
	}
	if len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		// resp decoded successfully (CreateChatCompletion returned no
		// error), so resp.Usage is known even though there is no usable
		// choice — see anthropic.go's identical comment.
		return CompletionResult{TokensUsed: resp.Usage.TotalTokens}, markUnusableResponse(providerErrorf(o.Name(), "empty response"))
	}

	return CompletionResult{
		Text:       resp.Choices[0].Message.Content,
		TokensUsed: resp.Usage.TotalTokens,
	}, nil
}

func orDefault(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

func orDefaultStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func doer(d Doer) Doer {
	if d == nil {
		return http.DefaultClient
	}
	return d
}
