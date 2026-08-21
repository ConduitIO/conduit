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
	"bytes"
	"context"
	"net/http"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

// DefaultAnthropicModel is the recommended default. A code constant, not a doc
// value, so bumping it is a reviewable diff rather than a docs edit nobody
// notices.
const DefaultAnthropicModel = "claude-sonnet-5"

// anthropicVersion is the required API version header. Anthropic's API is
// versioned by date and this header is mandatory.
const anthropicVersion = "2023-06-01"

const defaultAnthropicBaseURL = "https://api.anthropic.com"

// Anthropic is a single-turn adapter over the Messages API.
//
// A hand-rolled net/http client rather than an SDK: this is one JSON POST, and
// an SDK would be a heavy dependency for one method. See the package doc.
type Anthropic struct {
	APIKey  string
	Model   string
	BaseURL string
	HTTP    Doer
	Timeout time.Duration
}

func (a *Anthropic) Name() string { return NameAnthropic }

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (a *Anthropic) Complete(ctx context.Context, req CompletionRequest) (CompletionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, orDefault(a.Timeout, DefaultTimeout))
	defer cancel()

	body, err := json.Marshal(anthropicRequest{
		Model:     orDefaultStr(req.Model, orDefaultStr(a.Model, DefaultAnthropicModel)),
		MaxTokens: DefaultMaxTokens,
		System:    req.System,
		Messages:  []anthropicMessage{{Role: "user", Content: req.Prompt}},
	})
	if err != nil {
		return CompletionResult{}, providerErrorf(a.Name(), "encode request: %v", err)
	}

	url := strings.TrimSuffix(orDefaultStr(a.BaseURL, defaultAnthropicBaseURL), "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return CompletionResult{}, providerErrorf(a.Name(), "build request: %v", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", a.APIKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	resp, transportErr := doer(a.HTTP).Do(httpReq)
	if transportErr != nil {
		// MarkIfTimeout distinguishes "ctx's deadline expired" (DefaultTimeout,
		// or the capture harness's captureWallClockBudget) from every OTHER
		// transport-level miss — DNS failure, connection refused, TLS error —
		// which HTTPStatus also can't discriminate (see errTimeout's doc
		// comment, provider/http.go).
		return CompletionResult{}, MarkIfTimeout(providerErrorf(a.Name(), "%v", transportErr), transportErr)
	}
	defer resp.Body.Close()

	if err := checkStatus(a.Name(), resp); err != nil {
		return CompletionResult{}, err
	}

	var out anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CompletionResult{}, markUnusableResponse(providerErrorf(a.Name(), "decode response: %v", err))
	}

	// Concatenate every text block. A response split across blocks is valid,
	// and taking only the first would silently truncate the config.
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	text := sb.String()
	tokensUsed := out.Usage.InputTokens + out.Usage.OutputTokens
	if strings.TrimSpace(text) == "" {
		// A refusal is still a BILLED call: out.Usage decoded successfully
		// above even though the content array came back empty, so the
		// token count IS known here — carried on CompletionResult even
		// though this returns an error, so a caller that inspects it on
		// error (captureProvider.Complete) never undercounts real spend.
		return CompletionResult{TokensUsed: tokensUsed}, markUnusableResponse(providerErrorf(a.Name(), "empty response"))
	}

	return CompletionResult{
		Text:       text,
		TokensUsed: tokensUsed,
	}, nil
}
