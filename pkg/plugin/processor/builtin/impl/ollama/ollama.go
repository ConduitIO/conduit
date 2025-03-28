// Copyright © 2025 Meroxa, Inc.
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
//go:generate paramgen -output=paramgen_command.go requestProcessorConfig

package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// limiting to llama3.2 for MVP
var allowedModels = []string{
	"llama3.2",
}

type requestProcessorConfig struct {
	// OllamaURL is the url to the ollama instance
	OllamaURL string `json:"url" validate:"required"`
	// Model is the name of the model used with ollama
	Model string `json:"model" default:"llama3.2"`
	// Prompt is the prompt to pass into ollama to tranform the data
	Prompt string `json:"prompt" default:""`
}

type requestProcessor struct {
	sdk.UnimplementedProcessor

	logger log.CtxLogger
	config requestProcessorConfig
}

func NewRequestProcessor(l log.CtxLogger) sdk.Processor {
	return &requestProcessor{logger: l.WithComponent("ollama.request")}
}

func (p *requestProcessor) Configure(ctx context.Context, cfg config.Config) error {
	err := sdk.ParseConfig(ctx, cfg, &p.config, requestProcessorConfig{}.Parameters())
	if err != nil {
		return fmt.Errorf("failed to parse configuration: %w", err)
	}

	return nil
}

func (p *requestProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "ollama.request",
		Summary:     "Processes data through an ollama instance",
		Description: "This processor transforms data by asking the provided model on the provided ollama instance.",
		Version:     "v0.0.1",
		Author:      "Sarah Sicard",
		Parameters:  p.config.Parameters(),
	}, nil
}

func (p *requestProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	logger := sdk.Logger(ctx)
	result := make([]sdk.ProcessedRecord, 0, len(records))

	if !slices.Contains(allowedModels, p.config.Model) {
		return append(result, sdk.ErrorRecord{Error: fmt.Errorf("model not allowed by processor")})
	}

	for _, rec := range records {
		reqBody, err := p.constructOllamaRequest(rec)
		if err != nil {
			return append(result, sdk.ErrorRecord{Error: fmt.Errorf("creating the ollama request %w", err)})
		}
		logger.Debug().Msg(fmt.Sprintf("Ollama Request: %s", reqBody))

		resp, err := p.sendOllamaRequest(reqBody)
		if err != nil {
			return append(result, sdk.ErrorRecord{Error: fmt.Errorf("error sending the ollama request %w", err)})
		}

		respData, err := processOllamaResponse(resp)
		if err != nil {
			logger.Error().Err(err).Msg("cannot process response")
			return append(result, sdk.ErrorRecord{Error: fmt.Errorf("cannot process response %w", err)})
		}
		rec.Payload.After = respData

		result = append(result, sdk.SingleRecord(rec))
	}

	logger.Debug().Msg(fmt.Sprintf("Processed Records: %s", result))
	return result
}

func (p *requestProcessor) constructOllamaRequest(rec opencdc.Record) ([]byte, error) {
	prompt, err := generatePrompt(p.config.Prompt, rec.Payload)
	if err != nil {
		return nil, err
	}

	data := map[string]interface{}{
		"model":  p.config.Model,
		"prompt": prompt,
		"format": "json",
		"stream": false,
	}
	reqBody, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling json %w", err)
	}
	return reqBody, nil
}

func (p *requestProcessor) sendOllamaRequest(reqBody []byte) ([]byte, error) {
	baseURL := fmt.Sprintf("%s/api/generate", p.config.OllamaURL)
	req, err := http.NewRequest("POST", baseURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("unable to create ollama request %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to ollama failed %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading body of call %w", err)
	}

	return body, nil
}

func generatePrompt(userPrompt string, record opencdc.Change) (string, error) {
	conduitInstructions := "For the prompt, return a valid json following the instructions provided. Only send back records in the json format with no explanation."
	prompt := fmt.Sprintf(
		"Instructions: {%s}\n Record: {%s} \n Suffix {%s}",
		userPrompt,
		record,
		conduitInstructions)

	err := validatePrompt(prompt)
	if err != nil {
		return "", fmt.Errorf("prompt validation failed %w", err)
	}

	return prompt, nil
}

type OllamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// processOllamaResponse parses the response from the ollama call and returns only the desired response
// as opencdc.StructuredData
func processOllamaResponse(body []byte) (opencdc.StructuredData, error) {
	var response OllamaResponse
	err := json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON in ollama response: %w", err)
	}

	if !response.Done {
		return nil, fmt.Errorf("error response from ollama %w", err)
	}

	var returnData opencdc.StructuredData
	err = json.Unmarshal([]byte(response.Response), &returnData)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON in response: %w", err)
	}

	return returnData, nil
}

type PromptValidationConfig struct {
	MaxLength       int
	MinLength       int
	BlockedPatterns []string
}

// validatePrompt will perform some security checks against the user input
func validatePrompt(input string) error {
	config := PromptValidationConfig{
		MaxLength: 4096,
		MinLength: 3,
		BlockedPatterns: []string{
			"rm -rf",
			"DROP TABLE",
			"<script>",
			"javascript:",
			"data:text/html",
		},
	}

	if len(input) < config.MinLength {
		return fmt.Errorf("prompt is too short, must be %d characters", config.MinLength)
	}

	if len(input) > config.MaxLength {
		return fmt.Errorf("prompt exceeds context window allowance, must be %d characters", config.MaxLength)
	}

	for _, pattern := range config.BlockedPatterns {
		if strings.Contains(strings.ToLower(input), strings.ToLower(pattern)) {
			return fmt.Errorf("potentially malicious pattern: %s", pattern)
		}
	}

	return nil
}
