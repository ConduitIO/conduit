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
//go:generate paramgen -output=paramgen_command.go ollamaProcessorConfig
//go:generate go run go.uber.org/mock/mockgen  --build_flags=--mod=mod -source=./ollama.go -destination=mock/http_client_mock.go -package=mock

package ollama

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/rs/zerolog"
)

var HTTPClient httpClient = http.DefaultClient

type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// limiting to llama3.2 for MVP
var allowedModels = []string{
	"llama3.2",
}

type ollamaProcessorConfig struct {
	// Field is the reference to the field to process. Defaults to ".Payload.After".
	Field string `json:"field" validate:"regex=^.Payload" default:".Payload.After"`
	// OllamaURL is the url to the ollama instance
	OllamaURL string `json:"url" validate:"required"`
	// Model is the name of the model used with ollama
	Model string `json:"model" default:"llama3.2"`
	// Prompt is the prompt to pass into ollama to tranform the data
	Prompt string `json:"prompt" default:""`
}

type ollamaProcessor struct {
	sdk.UnimplementedProcessor

	config      ollamaProcessorConfig
	logger      log.CtxLogger
	fieldRefRes sdk.ReferenceResolver
}

type OllamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

func NewOllamaProcessor(l log.CtxLogger) sdk.Processor {
	return &ollamaProcessor{logger: l.WithComponent("ollama")}
}

func (p *ollamaProcessor) Configure(ctx context.Context, cfg config.Config) error {
	err := sdk.ParseConfig(ctx, cfg, &p.config, ollamaProcessorConfig{}.Parameters())
	if err != nil {
		return fmt.Errorf("failed to parse configuration: %w", err)
	}

	p.fieldRefRes, err = sdk.NewReferenceResolver(p.config.Field)
	if err != nil {
		return cerrors.Errorf("failed to create reference resolver: %w", err)
	}

	return nil
}

func (p *ollamaProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "ollama",
		Summary:     "Processes data through an ollama instance",
		Description: "This processor transforms data by asking the provided model on the provided ollama instance.",
		Version:     "v0.0.1",
		Author:      "Meroxa, Inc.",
		Parameters:  p.config.Parameters(),
	}, nil
}

func (p *ollamaProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	logger := sdk.Logger(ctx)
	processedRecords := make([]sdk.ProcessedRecord, 0, len(records))

	if !slices.Contains(allowedModels, p.config.Model) {
		return append(processedRecords, sdk.ErrorRecord{Error: fmt.Errorf("model {%s} not allowed by processor", p.config.Model)})
	}

	for _, rec := range records {
		processedRecord, err := p.processRecord(ctx, rec, logger)
		if err != nil {
			return append(processedRecords, sdk.ErrorRecord{Error: err})
		}

		processedRecords = append(processedRecords, processedRecord)
	}

	return processedRecords
}

func (p *ollamaProcessor) processRecord(ctx context.Context, rec opencdc.Record, logger *zerolog.Logger) (sdk.ProcessedRecord, error) {
	ref, err := p.fieldRefRes.Resolve(&rec)
	if err != nil {
		return nil, cerrors.Errorf("failed resolving reference: %w", err)
	}

	field := ref.Get()
	data, err := p.structuredData(field)
	if err != nil {
		return nil, cerrors.Errorf("cannot process field: %w", err)
	}

	reqBody, err := p.constructOllamaRequest(data)
	if err != nil {
		return nil, cerrors.Errorf("creating the ollama request %w", err)
	}
	logger.Debug().Msg(fmt.Sprintf("Ollama Request: %s", reqBody))

	resp, err := p.sendOllamaRequest(ctx, reqBody)
	if err != nil {
		return nil, cerrors.Errorf("error sending the ollama request %s", err)
	}

	respData, err := processOllamaResponse(resp)
	if err != nil {
		return nil, cerrors.Errorf("cannot process response: %w", err)
	}

	err = ref.Set(respData)
	if err != nil {
		return nil, cerrors.Errorf("error setting the designated field %w", err)
	}

	return sdk.SingleRecord(rec), nil
}

func (p *ollamaProcessor) constructOllamaRequest(rec opencdc.StructuredData) ([]byte, error) {
	prompt, err := generatePrompt(p.config.Prompt, rec)
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

func (p *ollamaProcessor) sendOllamaRequest(ctx context.Context, reqBody []byte) ([]byte, error) {
	baseURL := fmt.Sprintf("%s/api/generate", p.config.OllamaURL)
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("unable to create ollama request %w", err)
	}

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to ollama failed %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama request returned status code %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading body of call %w", err)
	}

	return body, nil
}

func generatePrompt(userPrompt string, record opencdc.StructuredData) (string, error) {
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

func (p *ollamaProcessor) structuredData(data any) (opencdc.StructuredData, error) {
	var sd opencdc.StructuredData
	switch v := data.(type) {
	case opencdc.RawData:
		b := v.Bytes()
		// if data is empty, then return empty structured data
		if len(b) == 0 {
			return sd, nil
		}
		err := json.Unmarshal(b, &sd)
		if err != nil {
			return nil, cerrors.Errorf("failed unmarshalling JSON from raw data: %w", err)
		}
	case string:
		err := json.Unmarshal([]byte(v), &sd)
		if err != nil {
			return nil, cerrors.Errorf("failed unmarshalling JSON from string: %w", err)
		}
	case []byte:
		err := json.Unmarshal(v, &sd)
		if err != nil {
			return nil, cerrors.Errorf("failed unmarshalling JSON from []byte: %w", err)
		}
	case opencdc.StructuredData:
		sd = v
	case nil:
		return nil, cerrors.Errorf("field to unmarshal is nil")
	default:
		return nil, cerrors.Errorf("unexpected data type %T", v)
	}

	return sd, nil
}
