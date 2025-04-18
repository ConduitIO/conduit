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
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
	"github.com/sashabaranov/go-openai"
)

func TestEmbeddingsProcessor_Configure(t *testing.T) {
	is := is.New(t)
	p := NewEmbeddingsProcessor(log.Nop())

	cfg := config.Config{
		embeddingsConfigApiKey: "test-api-key",
		embeddingsConfigModel:  "text-embedding-3-small",
		embeddingsConfigField:  ".Payload.After",
	}

	ctx := context.Background()

	err := p.Configure(ctx, cfg)
	is.NoErr(err)

	err = p.Configure(ctx, config.Config{})
	is.True(err != nil)
}

func TestEmbeddingsProcessor_Process(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	processor := NewEmbeddingsProcessor(log.Nop())
	cfg := config.Config{
		embeddingsConfigApiKey: "fake api key",
		embeddingsConfigModel:  string(openai.SmallEmbedding3),
	}

	is.NoErr(processor.Configure(ctx, cfg))

	mockEmbeddings := []float32{0.1, 0.2, 0.3, 0.4, 0.5}
	processor.call = &mockEmbeddingsCaller{Embeddings: mockEmbeddings}

	rec := opencdc.Record{
		Payload: opencdc.Change{
			After: opencdc.RawData("test text"),
		},
	}

	processed := processor.Process(ctx, []opencdc.Record{rec})
	is.Equal(len(processed), 1)

	_, isError := processed[0].(sdk.ErrorRecord)
	is.Equal(isError, false)

	processedRec, ok := processed[0].(sdk.SingleRecord)
	is.True(ok)

	record := opencdc.Record(processedRec)
	var embeddingsResult []float32
	err := json.Unmarshal(record.Payload.After.Bytes(), &embeddingsResult)
	is.NoErr(err)
	is.Equal(embeddingsResult, mockEmbeddings)
}

func TestEmbeddingsProcessorWithRetry(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	processor := NewEmbeddingsProcessor(log.Nop())

	cfg := config.Config{
		embeddingsConfigApiKey:         "fake api key",
		embeddingsConfigModel:          "text-embedding-3-small",
		embeddingsConfigMaxRetries:     "3",
		embeddingsConfigInitialBackoff: "10",
		embeddingsConfigMaxBackoff:     "100",
		embeddingsConfigBackoffFactor:  "2.0",
	}

	is.NoErr(processor.Configure(ctx, cfg))

	retryClient := &flakyEmbeddingsCaller{}
	processor.call = retryClient

	rec := opencdc.Record{
		Payload: opencdc.Change{
			After: opencdc.RawData("test text"),
		},
	}

	processor.Process(ctx, []opencdc.Record{rec})

	// We expect 2 calls: 1 initial attempt that fails + 1 retry that succeeds
	is.Equal(retryClient.CallCount, 2)
}
