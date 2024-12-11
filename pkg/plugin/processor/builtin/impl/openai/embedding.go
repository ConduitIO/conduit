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

//go:generate paramgen -output=embedding_config_paramgen.go embeddingProcConfig

package openai

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/ai/azopenai"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/lang"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

const EmbeddingMetadataBase64 = "openai.embedding.base64"

type embeddingProcConfig struct {
	APIKey     string `json:"apiKey" validate:"required"`
	Endpoint   string `json:"endpoint" default:"https://api.openai.com/v1"`
	Model      string `json:"model" validate:"required,inclusion=gpt-4|gpt-4-turbo|gpt-3.5-turbo|text-davinci-003|text-davinci-002|text-curie-001|text-babbage-001|text-ada-001"`
	InputField string `json:"inputField" validate:"regex=^\\.(Payload|Key).*" default:".Payload.After"`
}

type embeddingProcessor struct {
	sdk.UnimplementedProcessor

	cfg embeddingProcConfig

	inputFieldRefResolver sdk.ReferenceResolver

	client *azopenai.Client
	logger log.CtxLogger
}

func NewEmbeddingProcessor(log log.CtxLogger) sdk.Processor {
	return &embeddingProcessor{
		logger: log.WithComponent("openai_embedding"),
	}
}

func (p *embeddingProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "openai.embedding",
		Summary:     "Generate OpenAI embeddings.",
		Description: "",
		Version:     "v0.1.0",
		Author:      "Meroxa, Inc.",
		Parameters:  embeddingProcConfig{}.Parameters(),
	}, nil
}

func (p *embeddingProcessor) Configure(ctx context.Context, c config.Config) error {
	cfg := embeddingProcConfig{}
	err := sdk.ParseConfig(ctx, c, &cfg, embeddingProcConfig{}.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}

	inputResolver, err := sdk.NewReferenceResolver(cfg.InputField)
	if err != nil {
		return cerrors.Errorf(`failed to create a field resolver for %v parameter: %w`, cfg.InputField, err)
	}
	p.inputFieldRefResolver = inputResolver

	p.cfg = cfg
	return nil
}

func (p *embeddingProcessor) Open(ctx context.Context) error {
	keyCredential := azcore.NewKeyCredential(p.cfg.APIKey)

	// NOTE: this constructor creates a client that connects to the public OpenAI endpoint.
	// To connect to an Azure OpenAI endpoint, use azopenai.NewClient() or azopenai.NewClientWithyKeyCredential.
	client, err := azopenai.NewClientForOpenAI("https://api.openai.com/v1", keyCredential, nil)
	if err != nil {
		return cerrors.Errorf("failed to create OpenAI client: %w", err)
	}

	p.client = client
	return nil
}

func (p *embeddingProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))

	// Prepare request (batch all embedding inputs)
	var embeddingInputs []string
	for _, record := range records {
		inRef, err := p.inputFieldRefResolver.Resolve(&record)
		if err != nil {
			out = append(out, sdk.ErrorRecord{Error: fmt.Errorf("failed to resolve reference %v: %w", p.cfg.InputField, err)})
			continue
		}

		embeddingInputs = append(embeddingInputs, p.getEmbeddingInput(inRef.Get()))
	}

	// Execute request (get embeddings)
	embeddings, err := p.client.GetEmbeddings(
		ctx,
		azopenai.EmbeddingsOptions{
			Input:          embeddingInputs,
			EncodingFormat: lang.Ptr(azopenai.EmbeddingEncodingFormatBase64),
			DeploymentName: &p.cfg.Model,
		},
		nil,
	)

	// If the request failed, declare processing for all records as failed
	if err != nil {
		for range len(records) {
			out = append(out, sdk.ErrorRecord{Error: err})
		}

		return out
	}

	p.logger.Trace(ctx).
		Any("embedding_input", embeddingInputs).
		Any("embedding_output", embeddings).
		Msg("got embeddings")

	for i, record := range records {
		record.Metadata[EmbeddingMetadataBase64] = embeddings.Data[i].EmbeddingBase64
		// todo add more metadata related to the embeddings
		out = append(out, sdk.SingleRecord(record))
	}

	return out
}

func (p *embeddingProcessor) Teardown(context.Context) error {
	return nil
}

func (p *embeddingProcessor) getEmbeddingInput(val any) string {
	switch v := val.(type) {
	case opencdc.RawData:
		return string(v)
	case opencdc.StructuredData:
		return string(v.Bytes())
	default:
		return fmt.Sprintf("%v", v)
	}
}
