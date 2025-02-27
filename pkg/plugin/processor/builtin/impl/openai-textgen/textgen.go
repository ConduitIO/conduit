// Copyright © 2024 Meroxa, Inc.
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

package textgen

import (
	"context"
	"fmt"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/jpillora/backoff"
	"github.com/sashabaranov/go-openai"
)

//go:generate paramgen -output=paramgen_proc.go textgenProcessorConfig

type textgenProcessor struct {
	sdk.UnimplementedProcessor

	config            textgenProcessorConfig
	call              openaiCall
	referenceResolver sdk.ReferenceResolver
}

type openaiCall interface {
	Call(ctx context.Context, input string) (output string, err error)
}

type textgenProcessorConfig struct {
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
	// MaxRetries is the maximum number of retries for API calls. Defaults to 3.
	MaxRetries int `json:"max_retries" default:"3"`
	// InitialBackoff is the initial backoff duration in milliseconds. Defaults to 1000ms (1s).
	InitialBackoff int `json:"initial_backoff" default:"1000"`
	// MaxBackoff is the maximum backoff duration in milliseconds. Defaults to 30000ms (30s).
	MaxBackoff int `json:"max_backoff" default:"30000"`
	// BackoffFactor is the factor by which the backoff increases. Defaults to 2.
	BackoffFactor float64 `json:"backoff_factor" default:"2.0"`
}

func NewTextgenProcessor(l log.CtxLogger) sdk.Processor {
	return sdk.ProcessorWithMiddleware(
		&textgenProcessor{},
		sdk.DefaultProcessorMiddleware()...,
	)
}

func (p *textgenProcessor) Configure(ctx context.Context, cfg config.Config) error {
	err := sdk.ParseConfig(ctx, cfg, &p.config, textgenProcessorConfig{}.Parameters())
	if err != nil {
		return fmt.Errorf("failed to parse configuration: %w", err)
	}

	p.referenceResolver, err = sdk.NewReferenceResolver(p.config.Field)
	if err != nil {
		return fmt.Errorf("failed to create reference resolver: %w", err)
	}

	p.call = &openaiClient{
		client: openai.NewClient(p.config.APIKey),
		config: &p.config,
	}

	return nil
}

func (p *textgenProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "openai-textgen",
		Summary:     "modify records using openai models",
		Description: "textgen is a conduit processor that will transform a record based on a given prompt",
		Version:     "devel",
		Author:      "Meroxa, Inc.",
		Parameters:  textgenProcessorConfig{}.Parameters(),
	}, nil
}

func (p *textgenProcessor) Process(ctx context.Context, recs []opencdc.Record) []sdk.ProcessedRecord {
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

func (p *textgenProcessor) processRecord(ctx context.Context, rec opencdc.Record) (opencdc.Record, error) {
	logger := sdk.Logger(ctx)

	ref, err := p.referenceResolver.Resolve(&rec)
	if err != nil {
		return rec, fmt.Errorf("failed to resolve reference: %w", err)
	}

	val := ref.Get()

	var payload string
	switch v := val.(type) {
	case opencdc.Position:
		payload = string(v)

		res, err := p.callOpenAI(ctx, payload)
		if err != nil {
			return rec, fmt.Errorf("failed to create chat completion: %w", err)
		}

		logger.Trace().Msgf("processed record position %s", res)

		if err := ref.Set(opencdc.Position(res)); err != nil {
			return rec, fmt.Errorf("failed to set position: %w", err)
		}
	case opencdc.Data:
		payload = string(v.Bytes())

		res, err := p.callOpenAI(ctx, payload)
		if err != nil {
			return rec, fmt.Errorf("failed to create chat completion: %w", err)
		}

		logger.Trace().Msgf("processed record data %s", res)

		var data opencdc.Data = opencdc.RawData(res)

		if err := ref.Set(data); err != nil {
			return rec, fmt.Errorf("failed to set data: %w", err)
		}

	case string:
		payload = v

		res, err := p.callOpenAI(ctx, payload)
		if err != nil {
			return rec, fmt.Errorf("failed to create chat completion: %w", err)
		}

		logger.Trace().Msgf("processed record string %s", res)

		if err := ref.Set(res); err != nil {
			return rec, fmt.Errorf("failed to set data: %w", err)
		}
	default:
		return rec, fmt.Errorf("unsupported type %T", v)
	}

	return rec, nil
}

func (p *textgenProcessor) callOpenAI(ctx context.Context, payload string) (string, error) {
	b := &backoff.Backoff{
		Min:    time.Duration(p.config.InitialBackoff) * time.Millisecond,
		Max:    time.Duration(p.config.MaxBackoff) * time.Millisecond,
		Factor: p.config.BackoffFactor,
		Jitter: true,
	}

	var res string
	var err error
	var attempt int

	logger := sdk.Logger(ctx)

	for {
		attempt++

		if attempt > p.config.MaxRetries+1 {
			return "", fmt.Errorf("exceeded maximum retries (%d): %w", p.config.MaxRetries, err)
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		res, err = p.call.Call(ctx, payload)
		if err == nil {
			if attempt > 1 {
				logger.Debug().Int("attempts", attempt).Msg("OpenAI API call succeeded after retries")
			}
			break
		}

		if !isRetryableError(err) {
			return "", fmt.Errorf("chat completion failed with non-retryable error: %w", err)
		}

		backoffDuration := b.Duration()
		logger.Warn().
			Int("attempt", attempt).
			Dur("backoff", backoffDuration).
			Err(err).
			Msg("OpenAI API call failed, retrying after backoff")

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoffDuration):
		}
	}

	return res, nil
}

type openaiClient struct {
	client *openai.Client
	config *textgenProcessorConfig
}

func (o *openaiClient) Call(ctx context.Context, payload string) (string, error) {
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

// isRetryableError determines if an error from the OpenAI API is retryable
func isRetryableError(err error) bool {
	var apiErr *openai.APIError
	if !cerrors.As(err, &apiErr) {
		return true
	}

	switch apiErr.HTTPStatusCode {
	case 429:
		return true
	case 500, 502, 503, 504:
		return true
	default:
		return false
	}
}
