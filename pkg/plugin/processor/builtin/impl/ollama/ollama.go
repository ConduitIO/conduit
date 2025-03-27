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
	"bufio"
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
	logger.Info().Msg("Processing ollama records")

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

		// logger.Info().Msg(fmt.Sprintf("Raw response from ollama call: %s", string(body)))

		respJson, err := processOllamaResponse(body, logger)
		logger.Info().Msg(fmt.Sprintf("processed response from ollama call: %s", respJson))
		if err != nil {
			logger.Error().Err(err).Msg("cannot process response")
			return append(result, sdk.ErrorRecord{Error: fmt.Errorf("cannot process response %w", err)})
		}
		rec.Payload.After = respJson
		logger.Info().Msg(fmt.Sprintf("Payload.After: %v", rec.Payload.After))

		result = append(result, sdk.SingleRecord(rec))
	}

	logger.Info().Msg(fmt.Sprintf("Result of processed records: %s", result))
	return result
}

func (p *requestProcessor) sendOllamaRequest(rec opencdc.Record, logger *zerolog.Logger) ([]byte, error) {
	prompt := generatePrompt(p.config.Prompt, rec.Payload, logger)

	baseURL := fmt.Sprintf("%s/api/generate", p.config.OllamaURL)
	data := map[string]string{
		"model":  p.config.Model,
		"prompt": prompt,
	}
	jsonData, err := json.Marshal(data)
	if err != nil {
		logger.Error().Err(err).Msg("error marshalling json")
		return nil, err
	}

	// logger.Info().Msg(baseURL)
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

func generatePrompt(userPrompt string, record opencdc.Change, logger *zerolog.Logger) string {
	// TODO securing against malicious prompts
	// TODO limit size of the input
	conduitPrefix := "For the following records, return a json list of records following the instructions provided. Only send back records in the json format with no explanation."

	logger.Info().Msg(fmt.Sprintf("incoming record: %v", record))

	prompt := fmt.Sprintf(
		"%s \n Instructions: {%s}\n Record: {%s}",
		conduitPrefix,
		userPrompt,
		record)
	logger.Info().Msg(fmt.Sprintf("Sending message to ollama with the following prompt: %s", prompt))

	return prompt
}

type StreamResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// construct response into a json - comes back as a list of json for each character
func processOllamaResponse(body []byte, logger *zerolog.Logger) (opencdc.StructuredData, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))

	var fullResp string
	var finalRespReceived bool

	for scanner.Scan() {
		var streamResp StreamResponse
		lineData := scanner.Bytes()

		if len(lineData) == 0 {
			continue
		}

		err := json.Unmarshal(lineData, &streamResp)
		if err != nil {
			return nil, fmt.Errorf("error parsing stream response: %w", err)
		}

		fullResp += streamResp.Response

		if streamResp.Done {
			finalRespReceived = true
			break
		}
	}

	if !finalRespReceived {
		return nil, fmt.Errorf("incomplete response stream")
	}

	logger.Info().Msg(fmt.Sprintf("response after stream: %s", fullResp))
	var returnData opencdc.StructuredData
	err := json.Unmarshal([]byte(fullResp), &returnData)
	if err != nil {
		logger.Error().Msg(fmt.Sprintf("invalid json in response: %s", err))
		return nil, fmt.Errorf("invalid JSON in response: %w", err)
	}

	return returnData, nil
}
