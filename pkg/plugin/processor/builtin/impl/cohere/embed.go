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

package cohere

import (
	"context"
	"fmt"
	"strings"
	"time"

	cohere "github.com/cohere-ai/cohere-go/v2"
	cohereClient "github.com/cohere-ai/cohere-go/v2/client"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/jpillora/backoff"
	"github.com/klauspost/compress/zstd"
)

const EmbedModelMetadata = "cohere.embed.model"

//go:generate paramgen -output=paramgen_embed_proc.go embedProcConfig

type embedProcConfig struct {
	// Model is one of the Cohere embed models.
	Model string `json:"model" default:"embed-english-v2.0"`
	// APIKey is the API key for Cohere api calls.
	APIKey string `json:"apiKey" validate:"required"`
	// Specifies the type of input passed to the model. Required for embed models v3 and higher.
	// Allowed values: search_document, search_query, classification, clustering, image.
	InputType string `json:"inputType"`
	// Maximum number of retries for an individual record when backing off following an error.
	BackoffRetryCount float64 `json:"backoffRetry.count" default:"0" validate:"gt=-1"`
	// The multiplying factor for each increment step.
	BackoffRetryFactor float64 `json:"backoffRetry.factor" default:"2" validate:"gt=0"`
	// The minimum waiting time before retrying.
	BackoffRetryMin time.Duration `json:"backoffRetry.min" default:"100ms"`
	// The maximum waiting time before retrying.
	BackoffRetryMax time.Duration `json:"backoffRetry.max" default:"5s"`
	// Specifies the field from which the request body should be created.
	InputField string `json:"inputField" default:".Payload.After"`
	// OutputField specifies in which field should the response body be saved.
	OutputField string `json:"outputField" default:".Payload.After"`
	// MaxTextsPerRequest controls the number of texts sent in each Cohere embedding API call (max 96)
	MaxTextsPerRequest int `json:"maxTextsPerRequest" default:"96"`
}

func (c *embedProcConfig) validate() error {
	// validate `inputType` for v3 or higher models.
	if !strings.Contains(c.Model, "v2") && c.InputType == "" {
		return cerrors.Errorf("inputType is required for model %q (v3 or higher)", c.Model)
	}

	return nil
}

// embedModel defines the interface for the Cohere embed client
type embedModel interface {
	embed(ctx context.Context, texts []string) ([][]float64, error)
}

type EmbedProcessor struct {
	sdk.UnimplementedProcessor

	inputFieldRefResolver  *sdk.ReferenceResolver
	outputFieldRefResolver *sdk.ReferenceResolver
	logger                 log.CtxLogger

	config     embedProcConfig
	backoffCfg *backoff.Backoff
	client     embedModel
}

func NewEmbedProcessor(l log.CtxLogger) *EmbedProcessor {
	return &EmbedProcessor{logger: l}
}

func (p *EmbedProcessor) Configure(ctx context.Context, cfg config.Config) error {
	// Configure is the first function to be called in a processor. It provides the processor
	// with the configuration that needs to be validated and stored to be used in other methods.
	// This method should not open connections or any other resources. It should solely focus
	// on parsing and validating the configuration itself.

	err := sdk.ParseConfig(ctx, cfg, &p.config, embedProcConfig{}.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}

	err = p.config.validate()
	if err != nil {
		return cerrors.Errorf("error validating configuration: %w", err)
	}

	return nil
}

func (p *EmbedProcessor) Open(ctx context.Context) error {
	inputResolver, err := sdk.NewReferenceResolver(p.config.InputField)
	if err != nil {
		return cerrors.Errorf(`failed to create a field resolver for %v parameter: %w`, p.config.InputField, err)
	}
	p.inputFieldRefResolver = &inputResolver

	outputResolver, err := sdk.NewReferenceResolver(p.config.OutputField)
	if err != nil {
		return cerrors.Errorf(`failed to create a field resolver for %v parameter: %w`, p.config.OutputField, err)
	}
	p.outputFieldRefResolver = &outputResolver

	// Initialize the client only if it hasn't been injected
	if p.client == nil {
		p.client = &embedClient{
			client: cohereClient.NewClient(),
			config: &p.config,
		}
	}

	p.backoffCfg = &backoff.Backoff{
		Factor: p.config.BackoffRetryFactor,
		Min:    p.config.BackoffRetryMin,
		Max:    p.config.BackoffRetryMax,
	}

	return nil
}

func (p *EmbedProcessor) Specification() (sdk.Specification, error) {
	// Specification contains the metadata for the processor, which can be used to define how
	// to reference the processor, describe what the processor does and the configuration
	// parameters it expects.

	return sdk.Specification{
		Name:    "cohere.embed",
		Summary: "Conduit processor for Cohere's embed model.",
		Description: "The Cohere embed processor extracts text from the configured inputField, generates embeddings " +
			"using Cohere's embedding model, and stores the result in the configured outputField. " +
			"The embeddings are compressed using the zstd algorithm for efficient storage and transmission.",
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: embedProcConfig{}.Parameters(),
	}, nil
}

func (p *EmbedProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))

	// Process records in batches
	for i := 0; i < len(records); i += p.config.MaxTextsPerRequest {
		// Calculate end index for current batch
		end := min(i+p.config.MaxTextsPerRequest, len(records))

		batchRecords := records[i:end]
		batchResults, err := p.processBatch(ctx, batchRecords)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, batchResults...)
	}

	return out
}

func (p *EmbedProcessor) processBatch(ctx context.Context, records []opencdc.Record) ([]sdk.ProcessedRecord, error) {
	out := make([]sdk.ProcessedRecord, 0, len(records))

	// prepare embeddingInputs for request
	var embeddingInputs []string
	for _, record := range records {
		inRef, err := p.inputFieldRefResolver.Resolve(&record)
		if err != nil {
			return out, cerrors.Errorf("failed to resolve reference %v: %w", p.config.InputField, err)
		}

		input, err := p.getInput(inRef.Get())
		if err != nil {
			return out, cerrors.Errorf("failed to get input: %w", err)
		}
		embeddingInputs = append(embeddingInputs, input)
	}

	var embeddings [][]float64
	for {
		var err error
		// execute request to get embeddings
		embeddings, err = p.client.embed(ctx, embeddingInputs)

		attempt := p.backoffCfg.Attempt()
		duration := p.backoffCfg.Duration()

		if err != nil {
			switch {
			case cerrors.As(err, &cohere.GatewayTimeoutError{}),
				cerrors.As(err, &cohere.InternalServerError{}),
				cerrors.As(err, &cohere.ServiceUnavailableError{}):
				if attempt < p.config.BackoffRetryCount {
					p.logger.Debug(ctx).Err(err).Float64("attempt", attempt).
						Float64("backoffRetry.count", p.config.BackoffRetryCount).
						Int64("backoffRetry.duration", duration.Milliseconds()).
						Msg("retrying Cohere HTTP request")
					select {
					case <-ctx.Done():
						return out, ctx.Err()
					case <-time.After(duration):
						continue
					}
				} else {
					return out, cerrors.Errorf("failed to get embeddings: %w", err)
				}
			default:
				// BadRequestError, ClientClosedRequestError, ForbiddenError, InvalidTokenError,
				// NotFoundError, NotImplementedError, TooManyRequestsError, UnauthorizedError, UnprocessableEntityError
				return out, cerrors.Errorf("failed to get embeddings: %w", err)
			}
		}

		p.backoffCfg.Reset()
		break
	}

	for i, record := range records {
		// Add model name to metadata
		record.Metadata[EmbedModelMetadata] = p.config.Model

		embeddingJSON, err := json.Marshal(embeddings[i])
		if err != nil {
			return out, cerrors.Errorf("failed to marshal embeddings: %w", err)
		}

		// Compress the embedding using zstd
		compressedEmbedding, err := compressData(embeddingJSON)
		if err != nil {
			return out, cerrors.Errorf("failed to compress embeddings: %w", err)
		}

		if err := p.setField(&record, p.outputFieldRefResolver, compressedEmbedding); err != nil {
			return out, cerrors.Errorf("failed to set output: %w", err)
		}

		out = append(out, sdk.SingleRecord(record))
	}

	return out, nil
}

func (p *EmbedProcessor) setField(r *opencdc.Record, refRes *sdk.ReferenceResolver, data any) error {
	if refRes == nil {
		return nil
	}

	ref, err := refRes.Resolve(r)
	if err != nil {
		return cerrors.Errorf("error reference resolver: %w", err)
	}

	err = ref.Set(data)
	if err != nil {
		return cerrors.Errorf("error reference set: %w", err)
	}

	return nil
}

func (p *EmbedProcessor) getInput(val any) (string, error) {
	switch v := val.(type) {
	case opencdc.Position:
		return string(v), nil
	case opencdc.Data:
		return string(v.Bytes()), nil
	case string:
		return v, nil
	default:
		return "", fmt.Errorf("unsupported type %T", v)
	}
}

// compressData compresses the input data using zstd
func compressData(data []byte) ([]byte, error) {
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, cerrors.Errorf("failed to create zstd encoder: %w", err)
	}
	defer encoder.Close()

	return encoder.EncodeAll(data, nil), nil
}

// embedClient implements the embedModel interface
type embedClient struct {
	client *cohereClient.Client
	config *embedProcConfig
}

func (e *embedClient) embed(ctx context.Context, texts []string) ([][]float64, error) {
	// prepare request
	req := &cohere.V2EmbedRequest{
		Model:          e.config.Model,
		Texts:          texts,
		EmbeddingTypes: []cohere.EmbeddingType{cohere.EmbeddingTypeFloat},
	}
	if e.config.InputType != "" {
		req.InputType = cohere.EmbedInputType(e.config.InputType)
	}

	resp, err := e.client.V2.Embed(ctx, req, cohereClient.WithToken(e.config.APIKey))
	if err != nil {
		return nil, err
	}

	return resp.GetEmbeddings().GetFloat(), nil
}
