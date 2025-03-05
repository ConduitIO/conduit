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

package openaiwrap

import (
	"context"
	"strings"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/sashabaranov/go-openai"
)

func TestRecords() []opencdc.Record {
	return []opencdc.Record{
		{
			Operation: opencdc.OperationCreate,
			Key:       opencdc.RawData("key1"),
			Payload: opencdc.Change{
				Before: opencdc.RawData("bef-rec-1"),
				After:  opencdc.RawData("aft-rec-1"),
			},
		},
		{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.RawData("key2"),
			Payload: opencdc.Change{
				Before: opencdc.RawData("bef-rec-2"),
				After:  opencdc.RawData("aft-rec-2"),
			},
		},
		{
			Operation: opencdc.OperationDelete,
			Key:       opencdc.RawData("key3"),
			Payload: opencdc.Change{
				Before: opencdc.RawData("bef-rec-3"),
				After:  opencdc.RawData("aft-rec-3"),
			},
		},
	}
}

type FlakyOpenAICaller struct {
	CallCount int
}

func (f *FlakyOpenAICaller) Call(ctx context.Context, input string) (string, error) {
	f.CallCount++

	if f.CallCount < 2 {
		return "", &openai.APIError{HTTPStatusCode: 429, Message: "rate limit exceeded"}
	}

	return strings.ToUpper(input), nil
}

type MockEmbeddingsCaller struct {
	Embeddings []float32
}

func (m *MockEmbeddingsCaller) Call(ctx context.Context, input string) ([]float32, error) {
	if m.Embeddings == nil {
		// Default mock embeddings if none provided
		return []float32{0.1, 0.2, 0.3, 0.4, 0.5}, nil
	}
	return m.Embeddings, nil
}

type FlakyEmbeddingsCaller struct {
	CallCount  int
	Embeddings []float32
}

func (f *FlakyEmbeddingsCaller) Call(ctx context.Context, input string) ([]float32, error) {
	f.CallCount++

	if f.CallCount < 2 {
		return nil, &openai.APIError{HTTPStatusCode: 429, Message: "rate limit exceeded"}
	}

	if f.Embeddings == nil {
		return []float32{0.1, 0.2, 0.3, 0.4, 0.5}, nil
	}
	return f.Embeddings, nil
}
