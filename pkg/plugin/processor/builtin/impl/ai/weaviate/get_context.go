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

//go:generate paramgen -output=get_context_config_paramgen.go getContextConfig

package weaviate

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/weaviate/weaviate-go-client/v4/weaviate"
	"github.com/weaviate/weaviate-go-client/v4/weaviate/graphql"
)

type getContextConfig struct {
	ClassName    string `json:"className" validate:"required"`
	ContextField string `json:"contextField" validate:"required"`
}

type getContextProcessor struct {
	sdk.UnimplementedProcessor
	cfg    getContextConfig
	client *weaviate.Client
	logger log.CtxLogger
}

func NewGetContextProcessor(logger log.CtxLogger) sdk.Processor {
	return &getContextProcessor{
		logger: logger.WithComponent("weaviate.getContextProcessor"),
	}
}

func (p *getContextProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "weaviate.getContext",
		Summary:     "Get context from Weaviate",
		Description: "Get context from Weaviate",
		Version:     "v0.1.0",
		Author:      "Meroxa, Inc.",
		Parameters:  getContextConfig{}.Parameters(),
	}, nil
}

func (p *getContextProcessor) Configure(ctx context.Context, c config.Config) error {
	cfg := getContextConfig{}
	err := sdk.ParseConfig(ctx, c, &cfg, getContextConfig{}.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}

	p.cfg = cfg
	return nil
}

func (p *getContextProcessor) Open(ctx context.Context) error {
	// Configure Weaviate client
	cfg := weaviate.Config{
		Host:   "localhost:8080", // Replace with your Weaviate instance URL
		Scheme: "http",
	}
	client, err := weaviate.NewClient(cfg)
	if err != nil {
		return cerrors.Errorf("Failed to create Weaviate client: %v", err)
	}

	p.client = client
	return nil
}

func (p *getContextProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))

	for _, record := range records {
		out = append(out, p.setContext(ctx, record))
	}

	return out
}

func (p *getContextProcessor) Teardown(ctx context.Context) error {
	return nil
}

func (p *getContextProcessor) setContext(ctx context.Context, rec opencdc.Record) sdk.ProcessedRecord {
	embeddings, err := p.decodeEmbeddings(rec.Metadata["openai.embedding.base64"])
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("failed to decode embeddings: %w", err)}
	}

	textContext, err := p.getContext(ctx, embeddings)
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("failed to perform vector search: %w", err)}
	}

	rec.Metadata["weaviate.context"] = textContext

	return sdk.SingleRecord(rec)
}

func (p *getContextProcessor) decodeEmbeddings(base64Str string) ([]float32, error) {
	// Decode the base64 string
	decodedBytes, err := base64.StdEncoding.DecodeString(base64Str)
	if err != nil {
		return nil, err
	}

	// Convert byte slice to float32 slice
	embeddings := make([]float32, len(decodedBytes)/4)

	for i := 0; i < len(embeddings); i++ {
		// Convert 4 bytes to a single float32
		bits := binary.LittleEndian.Uint32(decodedBytes[i*4 : (i+1)*4])
		embeddings[i] = math.Float32frombits(bits)
	}

	return embeddings, nil
}

func (p *getContextProcessor) getContext(ctx context.Context, targetEmbedding []float32) (string, error) {
	// Prepare the GraphQL query for vector search
	fields := []graphql.Field{
		{Name: p.cfg.ContextField},
		{Name: "_additional { distance }"},
	}

	result, err := p.client.GraphQL().
		Get().
		WithClassName(p.cfg.ClassName).
		WithFields(fields...).
		WithNearVector(
			p.client.GraphQL().NearVectorArgBuilder().WithVector(targetEmbedding),
		).
		WithLimit(5).
		Do(ctx)
	if err != nil {
		return "", fmt.Errorf("vector search failed: %v", err)
	}

	// Process and print the results
	if result.Errors != nil {
		errs := make([]string, 0)
		for _, resultErr := range result.Errors {
			errs = append(errs, resultErr.Message)
		}
		return "", cerrors.Errorf("vector search failed: %v", strings.Join(errs, ", "))
	}

	data := result.Data["Get"].(map[string]interface{})[p.cfg.ClassName].([]interface{})

	sb := strings.Builder{}
	p.logger.Info(ctx).Msg("found closest texts")
	fmt.Println("Top 5 Closest Texts:")
	for _, item := range data {
		itemMap := item.(map[string]interface{})
		text := itemMap[p.cfg.ContextField].(string)

		sb.WriteString(text)
		sb.WriteRune('\n')
		sb.WriteRune('\n')
	}

	p.logger.Info(ctx).
		Str("context", sb.String()).
		Msg("context built")
	return sb.String(), nil
}
