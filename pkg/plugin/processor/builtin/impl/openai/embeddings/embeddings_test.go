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
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

type mockOpenAICall struct {
	embeddings []float32
	err        error
}

func (m *mockOpenAICall) Call(ctx context.Context, input string) ([]float32, error) {
	return m.embeddings, m.err
}

func TestEmbeddingsProcessor_Configure(t *testing.T) {
	is := is.New(t)
	p := &embeddingsProcessor{}

	cfg := config.Config{
		"api_key": "test-api-key",
		"model":   "text-embedding-3-small",
		"field":   ".Payload.After",
	}
	err := p.Configure(context.Background(), cfg)
	is.NoErr(err)

	err = p.Configure(context.Background(), config.Config{})
	is.True(err != nil)
}

func TestEmbeddingsProcessor_Process(t *testing.T) {
	is := is.New(t)
	p := &embeddingsProcessor{}

	cfg := config.Config{
		"api_key": "test-api-key",
		"model":   "text-embedding-3-small",
		"field":   ".Payload.After",
	}
	err := p.Configure(context.Background(), cfg)
	is.NoErr(err)

	mockEmbeddings := []float32{0.1, 0.2, 0.3, 0.4, 0.5}
	p.call = &mockOpenAICall{
		embeddings: mockEmbeddings,
		err:        nil,
	}

	rec := opencdc.Record{
		Payload: opencdc.Change{
			After: opencdc.RawData("test text"),
		},
	}

	ctx := context.Background()
	processed := p.Process(ctx, []opencdc.Record{rec})
	is.Equal(len(processed), 1)

	_, isError := processed[0].(sdk.ErrorRecord)
	is.Equal(isError, false)

	processedRec, ok := processed[0].(sdk.SingleRecord)
	is.True(ok)

	record := opencdc.Record(processedRec)
	var embeddingsResult []float32
	err = json.Unmarshal(record.Payload.After.Bytes(), &embeddingsResult)
	is.NoErr(err)
	is.Equal(embeddingsResult, mockEmbeddings)
}
