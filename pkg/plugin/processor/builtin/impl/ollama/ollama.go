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

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/rs/zerolog"
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

	if !slices.Contains(allowedModels, p.config.Model) {
		logger.Error().Msg("Model not allowed")
	}

	// need to loop through record by record in order to put an errors in the appropriate index for record
	result := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		body, err := p.sendOllamaRequest(rec, logger)
		if err != nil {
			return append(result, sdk.ErrorRecord{Error: fmt.Errorf("error sending the ollama request %w", err)})
		}

		respJson, err := processOllamaResponse(body, logger)
		if err != nil {
			logger.Error().Err(err).Msg("cannot process response")
			return append(result, sdk.ErrorRecord{Error: fmt.Errorf("cannot process response %w", err)})
		}
		rec.Payload.After = respJson

		result = append(result, sdk.SingleRecord(rec))
	}

	logger.Info().Msg(fmt.Sprintf("Result of processed records: %s", result))
	return result
}

func (p *requestProcessor) sendOllamaRequest(rec opencdc.Record, logger *zerolog.Logger) ([]byte, error) {
	prompt := generatePrompt(p.config.Prompt, rec.Payload)

	baseURL := fmt.Sprintf("%s/api/generate", p.config.OllamaURL)
	data := map[string]interface{}{
		"model":  p.config.Model,
		"prompt": prompt,
		"format": "json",
		"stream": false,
	}
	jsonData, err := json.Marshal(data)
	if err != nil {
		logger.Error().Err(err).Msg("error marshalling json")
		return nil, err
	}

	logger.Info().Msg(string(jsonData))
	req, err := http.NewRequest("POST", baseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		logger.Error().Err(err).Msg("unable to create request")
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Error().Err(err).Msg("sending the request failed")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error().Err(err).Msg("reading body of call")
		return nil, err
	}

	return body, nil
}

func generatePrompt(userPrompt string, record opencdc.Change) string {
	// TODO securing against malicious prompts
	// TODO limit size of the input
	conduitInstructions := "For the prompt, return a valid json following the instructions provided. Only send back records in the json format with no explanation."
	prompt := fmt.Sprintf(
		"Instructions: {%s}\n Record: {%s} \n Suffix {%s}",
		userPrompt,
		record,
		conduitInstructions)

	return prompt
}

type OllamaResponse struct {
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
	Response  string `json:"response"`
	Done      bool   `json:"done"`
}

// construct response into a json - comes back as a list of json for each character
func processOllamaResponse(body []byte, logger *zerolog.Logger) (opencdc.StructuredData, error) {
	logger.Info().Msg(fmt.Sprintf("raw json: %s", body))

	if !json.Valid(body) {
		fmt.Println("Invalid JSON")
	}

	// get the response from ollama
	var response OllamaResponse
	err := json.Unmarshal(body, &response)
	if err != nil {
		fmt.Println("Parsing error:", err)
		fmt.Println("Raw input:", body)
		return nil, fmt.Errorf("invalid JSON in ollama response: %w", err)
	}

	if !response.Done {
		return nil, fmt.Errorf("error response from ollama %w", err)
	}

	// parse out the response
	var returnData opencdc.StructuredData
	err = json.Unmarshal([]byte(response.Response), &returnData)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON in response: %w", err)
	}

	return returnData, nil
}
