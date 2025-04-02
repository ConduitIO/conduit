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
	"github.com/goccy/go-json"
	"github.com/sashabaranov/go-openai"
)

//go:generate paramgen -output=embeddings_paramgen.go embeddingsConfig

type EmbeddingsProcessor struct {
	sdk.UnimplementedProcessor

	config            embeddingsConfig
	call              openaiCaller[[]float32]
	referenceResolver sdk.ReferenceResolver
}

type embeddingsConfig struct {
	globalConfig

	// Field is the reference to the field to process. Defaults to ".Payload.After".
	Field string `json:"field" default:".Payload.After"`
	// APIKey is the OpenAI API key.
	APIKey string `json:"api_key" validate:"required"`
	// Model is the OpenAI embeddings model to use (e.g., text-embedding-3-small).
	Model string `json:"model" validate:"required"`
	// Dimensions is the number of dimensions the resulting output embeddings should have.
	Dimensions int `json:"dimensions"`
	// EncodingFormat is the format to return the embeddings in. Can be "float" or "base64".
	EncodingFormat string `json:"encoding_format"`
	// User is the user identifier for OpenAI API.
	User string `json:"user"`
}

func NewEmbeddingsProcessor(log.CtxLogger) *EmbeddingsProcessor {
	return &EmbeddingsProcessor{}
}

func (p *EmbeddingsProcessor) Configure(ctx context.Context, cfg config.Config) error {
	err := sdk.ParseConfig(ctx, cfg, &p.config, embeddingsConfig{}.Parameters())
	if err != nil {
		return fmt.Errorf("failed to parse configuration: %w", err)
	}

	p.referenceResolver, err = sdk.NewReferenceResolver(p.config.Field)
	if err != nil {
		return fmt.Errorf("failed to create reference resolver: %w", err)
	}

	if p.call == nil {
		p.call = &embeddingsCaller{
			client: openai.NewClient(p.config.APIKey),
			config: &p.config,
		}
	} else {
		sdk.Logger(ctx).Warn().Msg("openai API call was overriden with a custom implementation")
	}

	return nil
}

func (p *EmbeddingsProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "openai.embeddings",
		Summary:     "Generate embeddings for records using OpenAI models.",
		Description: "Embeddings is a Conduit processor that will generate vector embeddings for a record using OpenAI's embeddings API.",
		Version:     "v0.1.0",
		Author:      "Meroxa, Inc.",
		Parameters:  embeddingsConfig{}.Parameters(),
	}, nil
}

func (p *EmbeddingsProcessor) Process(ctx context.Context, recs []opencdc.Record) []sdk.ProcessedRecord {
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

func (p *EmbeddingsProcessor) processRecord(
	ctx context.Context, rec opencdc.Record,
) (opencdc.Record, error) {
	processor := func(ctx context.Context, input string) ([]float32, error) {
		return callWithRetry(ctx, p.config.globalConfig, p.call, input)
	}

	formatter := func(embeddings []float32) ([]byte, error) {
		embeddingsJSON, err := json.Marshal(embeddings)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal embeddings: %w", err)
		}
		return embeddingsJSON, nil
	}

	return ProcessRecordField(ctx, rec, p.referenceResolver, processor, formatter)
}

type embeddingsCaller struct {
	client *openai.Client
	config *embeddingsConfig
}

func (o *embeddingsCaller) Call(ctx context.Context, payload string) ([]float32, error) {
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
