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

package embeddings

import (
	"context"
	"encoding/json"
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

//go:generate paramgen -output=paramgen_proc.go embeddingsProcessorConfig

type embeddingsProcessor struct {
	sdk.UnimplementedProcessor

	config            embeddingsProcessorConfig
	call              openaiCall
	referenceResolver sdk.ReferenceResolver
}

type openaiCall interface {
	Call(ctx context.Context, input string) ([]float32, error)
}

type embeddingsProcessorConfig struct {
	// Field is the reference to the field to process. Defaults to ".Payload.After".
	Field string `json:"field" default:".Payload.After"`
	// APIKey is the OpenAI API key. Required.
	APIKey string `json:"api_key" validate:"required"`
	// Model is the OpenAI embeddings model to use (e.g., text-embedding-3-small). Required.
	Model string `json:"model" validate:"required"`
	// Dimensions is the number of dimensions the resulting output embeddings should have.
	Dimensions int `json:"dimensions"`
	// EncodingFormat is the format to return the embeddings in. Can be "float" or "base64".
	EncodingFormat string `json:"encoding_format"`
	// User is the user identifier for OpenAI API.
	User string `json:"user"`
	// MaxRetries is the maximum number of retries for API calls. Defaults to 3.
	MaxRetries int `json:"max_retries" default:"3"`
	// InitialBackoff is the initial backoff duration in milliseconds. Defaults to 1000ms (1s).
	InitialBackoff int `json:"initial_backoff" default:"1000"`
	// MaxBackoff is the maximum backoff duration in milliseconds. Defaults to 30000ms (30s).
	MaxBackoff int `json:"max_backoff" default:"30000"`
	// BackoffFactor is the factor by which the backoff increases. Defaults to 2.
	BackoffFactor float64 `json:"backoff_factor" default:"2.0"`
}

func NewEmbeddingsProcessor(l log.CtxLogger) sdk.Processor {
	return sdk.ProcessorWithMiddleware(
		&embeddingsProcessor{},
		sdk.DefaultProcessorMiddleware()...,
	)
}

func (p *embeddingsProcessor) Configure(ctx context.Context, cfg config.Config) error {
	err := sdk.ParseConfig(ctx, cfg, &p.config, embeddingsProcessorConfig{}.Parameters())
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

func (p *embeddingsProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "openai-embeddings",
		Summary:     "generate embeddings for records using openai models",
		Description: "embeddings is a conduit processor that will generate vector embeddings for a record using OpenAI's embeddings API",
		Version:     "0.1.0",
		Author:      "Meroxa, Inc.",
		Parameters:  embeddingsProcessorConfig{}.Parameters(),
	}, nil
}

func (p *embeddingsProcessor) Process(ctx context.Context, recs []opencdc.Record) []sdk.ProcessedRecord {
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

func (p *embeddingsProcessor) processRecord(ctx context.Context, rec opencdc.Record) (opencdc.Record, error) {
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

		embeddings, err := p.callOpenAI(ctx, payload)
		if err != nil {
			return rec, fmt.Errorf("failed to create embeddings: %w", err)
		}

		logger.Trace().Msgf("processed record position with embeddings of length %d", len(embeddings))

		// Convert embeddings to JSON
		embeddingsJSON, err := json.Marshal(embeddings)
		if err != nil {
			return rec, fmt.Errorf("failed to marshal embeddings: %w", err)
		}

		if err := ref.Set(opencdc.Position(embeddingsJSON)); err != nil {
			return rec, fmt.Errorf("failed to set position: %w", err)
		}
	case opencdc.Data:
		payload = string(v.Bytes())

		embeddings, err := p.callOpenAI(ctx, payload)
		if err != nil {
			return rec, fmt.Errorf("failed to create embeddings: %w", err)
		}

		logger.Trace().Msgf("processed record data with embeddings of length %d", len(embeddings))

		embeddingsJSON, err := json.Marshal(embeddings)
		if err != nil {
			return rec, fmt.Errorf("failed to marshal embeddings: %w", err)
		}

		var data opencdc.Data = opencdc.RawData(embeddingsJSON)

		if err := ref.Set(data); err != nil {
			return rec, fmt.Errorf("failed to set data: %w", err)
		}

	case string:
		payload = v

		embeddings, err := p.callOpenAI(ctx, payload)
		if err != nil {
			return rec, fmt.Errorf("failed to create embeddings: %w", err)
		}

		logger.Trace().Msgf("processed record string with embeddings of length %d", len(embeddings))

		// Convert embeddings to JSON
		embeddingsJSON, err := json.Marshal(embeddings)
		if err != nil {
			return rec, fmt.Errorf("failed to marshal embeddings: %w", err)
		}

		if err := ref.Set(string(embeddingsJSON)); err != nil {
			return rec, fmt.Errorf("failed to set data: %w", err)
		}
	default:
		return rec, fmt.Errorf("unsupported type %T", v)
	}

	return rec, nil
}

func (p *embeddingsProcessor) callOpenAI(ctx context.Context, payload string) ([]float32, error) {
	b := &backoff.Backoff{
		Min:    time.Duration(p.config.InitialBackoff) * time.Millisecond,
		Max:    time.Duration(p.config.MaxBackoff) * time.Millisecond,
		Factor: p.config.BackoffFactor,
		Jitter: true,
	}

	var embeddings []float32
	var err error
	var attempt int

	logger := sdk.Logger(ctx)

	for {
		attempt++

		if attempt > p.config.MaxRetries+1 {
			return nil, fmt.Errorf("exceeded maximum retries (%d): %w", p.config.MaxRetries, err)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		embeddings, err = p.call.Call(ctx, payload)
		if err == nil {
			if attempt > 1 {
				logger.Debug().Int("attempts", attempt).Msg("OpenAI API call succeeded after retries")
			}
			break
		}

		if !isRetryableError(err) {
			return nil, fmt.Errorf("embeddings creation failed with non-retryable error: %w", err)
		}

		backoffDuration := b.Duration()
		logger.Warn().
			Int("attempt", attempt).
			Dur("backoff", backoffDuration).
			Err(err).
			Msg("OpenAI API call failed, retrying after backoff")

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoffDuration):
		}
	}

	return embeddings, nil
}

type openaiClient struct {
	client *openai.Client
	config *embeddingsProcessorConfig
}

func (o *openaiClient) Call(ctx context.Context, payload string) ([]float32, error) {
	var embeddings []float32
	var err error

	resp, err := o.client.CreateEmbeddings(ctx, openai.EmbeddingRequest{
		Input:          payload,
		Model:          openai.EmbeddingModel(o.config.Model),
		EncodingFormat: openai.EmbeddingEncodingFormat(o.config.EncodingFormat),
		Dimensions:     o.config.Dimensions,
		User:           o.config.User,
	})
	if err != nil {
		return nil, err
	}

	if len(resp.Data) > 0 {
		embeddings = resp.Data[0].Embedding
	}

	return embeddings, nil
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
