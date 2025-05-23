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

package openai

import (
	"context"
	"fmt"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/sashabaranov/go-openai"
)

//go:generate paramgen -output=textgen_paramgen.go textgenConfig

type TextgenProcessor struct {
	sdk.UnimplementedProcessor

	config            textgenConfig
	call              openaiCaller[string]
	referenceResolver sdk.ReferenceResolver
}

type textgenConfig struct {
	globalConfig

	// Field is the reference to the field to process. Defaults to ".Payload.After".
	Field string `json:"field" default:".Payload.After"`
	// APIKey is the OpenAI API key. Required.
	APIKey string `json:"api_key" validate:"required"`
	// DeveloperMessage is the system message that guides the model's behavior. Required.
	DeveloperMessage string `json:"developer_message" validate:"required"`
	// StrictOutput enforces strict output format. Defaults to false.
	StrictOutput bool `json:"strict_output" default:"false"`
	// Model is the OpenAI model to use (e.g., gpt-4o-mini). Required.
	Model string `json:"model" validate:"required"`
	// MaxTokens is the maximum number of tokens to generate.
	MaxTokens int `json:"max_tokens"`
	// MaxCompletionTokens is the maximum number of tokens for completion.
	MaxCompletionTokens int `json:"max_completion_tokens"`
	// Temperature controls randomness (0-2, lower is more deterministic).
	Temperature float32 `json:"temperature"`
	// TopP controls diversity via nucleus sampling.
	TopP float32 `json:"top_p"`
	// N is the number of completions to generate.
	N int `json:"n"`
	// Stream is whether to stream the results or not. Not used for now.
	Stream bool `json:"stream"`
	// Stop are sequences where the API will stop generating.
	Stop []string `json:"stop"`
	// PresencePenalty penalizes new tokens based on presence in text.
	PresencePenalty float32 `json:"presence_penalty"`
	// Seed is the seed for deterministic results.
	Seed *int `json:"seed"`
	// FrequencyPenalty penalizes new tokens based on frequency in text.
	FrequencyPenalty float32 `json:"frequency_penalty"`
	// LogitBias modifies the likelihood of specified tokens appearing.
	LogitBias map[string]int `json:"logit_bias"`
	// LogProbs is whether to return log probabilities of output tokens.
	LogProbs bool `json:"log_probs"`
	// TopLogProbs is the number of most likely tokens to return probabilities for.
	TopLogProbs int `json:"top_log_probs"`
	// User is the user identifier for OpenAI API.
	User string `json:"user"`
	// Store is whether to store the conversation in OpenAI.
	Store bool `json:"store"`
	// ReasoningEffort controls the amount of reasoning in the response.
	ReasoningEffort string `json:"reasoning_effort"`
	// Metadata is additional metadata to include with the request.
	Metadata map[string]string `json:"metadata"`
}

func NewTextgenProcessor(log.CtxLogger) *TextgenProcessor {
	return &TextgenProcessor{}
}

func (p *TextgenProcessor) Configure(ctx context.Context, cfg config.Config) error {
	err := sdk.ParseConfig(ctx, cfg, &p.config, textgenConfig{}.Parameters())
	if err != nil {
		return fmt.Errorf("failed to parse configuration: %w", err)
	}

	p.referenceResolver, err = sdk.NewReferenceResolver(p.config.Field)
	if err != nil {
		return fmt.Errorf("failed to create reference resolver: %w", err)
	}

	if p.call == nil {
		p.call = &textgenCaller{
			client: openai.NewClient(p.config.APIKey),
			config: &p.config,
		}
	} else {
		sdk.Logger(ctx).Warn().Msg("openai API call was overriden with a custom implementation")
	}

	return nil
}

func (p *TextgenProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "openai.textgen",
		Summary:     "modify records using openai models",
		Description: "textgen is a conduit processor that will transform a record based on a given prompt",
		Version:     "v0.1.0",
		Author:      "Meroxa, Inc.",
		Parameters:  textgenConfig{}.Parameters(),
	}, nil
}

func (p *TextgenProcessor) Process(ctx context.Context, recs []opencdc.Record) []sdk.ProcessedRecord {
	var processedRecords []sdk.ProcessedRecord
	for _, rec := range recs {
		processed, err := p.processRecord(ctx, rec)
		if err != nil {
			return append(processedRecords, sdk.ErrorRecord{Error: err})
		}

		processedRecords = append(processedRecords, sdk.SingleRecord(processed))
	}

	return processedRecords
}

func (p *TextgenProcessor) processRecord(
	ctx context.Context, rec opencdc.Record,
) (opencdc.Record, error) {
	processor := func(ctx context.Context, input string) (string, error) {
		return callWithRetry(ctx, p.config.globalConfig, p.call, input)
	}

	formatter := func(result string) ([]byte, error) {
		return []byte(result), nil
	}

	return ProcessRecordField(ctx, rec, p.referenceResolver, processor, formatter)
}

type textgenCaller struct {
	client *openai.Client
	config *textgenConfig
}

func (o *textgenCaller) Call(ctx context.Context, payload string) (string, error) {
	res, err := o.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: o.config.Model,
		Messages: []openai.ChatCompletionMessage{
			{Role: "developer", Content: o.config.DeveloperMessage},
			{Role: "user", Content: payload},
		},
		MaxTokens:           o.config.MaxTokens,
		MaxCompletionTokens: o.config.MaxCompletionTokens,
		Temperature:         o.config.Temperature,
		TopP:                o.config.TopP,
		N:                   o.config.N,
		Stop:                o.config.Stop,
		PresencePenalty:     o.config.PresencePenalty,
		Seed:                o.config.Seed,
		FrequencyPenalty:    o.config.FrequencyPenalty,
		LogitBias:           o.config.LogitBias,
		LogProbs:            o.config.LogProbs,
		TopLogProbs:         o.config.TopLogProbs,
		User:                o.config.User,
		Store:               o.config.Store,
		ReasoningEffort:     o.config.ReasoningEffort,
	})
	if err != nil {
		return "", err
	}

	return res.Choices[0].Message.Content, nil
}
