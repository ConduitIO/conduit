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
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// DefaultOllamaModel is the recommended local default: a small,
// instruction-tuned model most laptops can actually run. The point of the
// ollama path is that it works with no API key, so the default must not assume
// a workstation GPU.
const DefaultOllamaModel = "llama3.1"

// Ollama is a single-turn adapter over a local Ollama server's /api/generate.
//
// Same hand-rolled net/http shape as the `ollama` built-in processor — see the
// package doc for why this is copied rather than forked.
type Ollama struct {
	Host    string
	Model   string
	HTTP    Doer
	Timeout time.Duration
}

func (o *Ollama) Name() string { return NameOllama }

type ollamaRequest struct {
	Model  string `json:"model"`
	System string `json:"system,omitempty"`
	Prompt string `json:"prompt"`
	// Stream is always false: this is a single-shot completion, and streaming
	// would mean parsing a JSONL body for no benefit — nothing renders tokens
	// as they arrive.
	Stream bool `json:"stream"`
}

type ollamaResponse struct {
	Response string `json:"response"`
	// Ollama reports both counts; a 0 means it did not report, never an
	// estimate.
	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

func (o *Ollama) Complete(ctx context.Context, req CompletionRequest) (CompletionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, orDefault(o.Timeout, DefaultTimeout))
	defer cancel()

	body, err := json.Marshal(ollamaRequest{
		Model:  orDefaultStr(req.Model, orDefaultStr(o.Model, DefaultOllamaModel)),
		System: req.System,
		Prompt: req.Prompt,
		Stream: false,
	})
	if err != nil {
		return CompletionResult{}, providerErrorf(o.Name(), "encode request: %v", err)
	}

	url := strings.TrimSuffix(orDefaultStr(o.Host, DefaultOllamaHost), "/") + "/api/generate"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return CompletionResult{}, providerErrorf(o.Name(), "build request: %v", err)
	}
	httpReq.Header.Set("content-type", "application/json")

	resp, err := doer(o.HTTP).Do(httpReq)
	if err != nil {
		return CompletionResult{}, providerErrorf(o.Name(), "%v", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(o.Name(), resp); err != nil {
		return CompletionResult{}, err
	}

	var out ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CompletionResult{}, providerErrorf(o.Name(), "decode response: %v", err)
	}
	if strings.TrimSpace(out.Response) == "" {
		return CompletionResult{}, providerErrorf(o.Name(), "empty response")
	}

	return CompletionResult{
		Text:       out.Response,
		TokensUsed: out.PromptEvalCount + out.EvalCount,
	}, nil
}

// Reachable reports whether an Ollama server answers at host. It is the Probe
// used by auto-detection (see Resolve).
//
// A HEAD against the root is enough: this answers "is something listening and
// speaking HTTP", not "is a model loaded". Pulling a model is the user's job
// and failing that is the completion call's error to report, with a message
// that says so — not a silent exclusion from auto-detection.
func Reachable(host string, client Doer, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), orDefault(timeout, 2*time.Second))
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, strings.TrimSuffix(host, "/")+"/", nil)
	if err != nil {
		return false
	}
	resp, err := doer(client).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode < 500
}
