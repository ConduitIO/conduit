package openaiwrap

import (
	"context"
	"strings"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/sashabaranov/go-openai"
)

// TestRecords returns a standard set of test records that can be used across processor tests
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

// MockOpenAICaller is a simple implementation of OpenaiCaller that returns uppercase input
type MockOpenAICaller struct{}

func (f *MockOpenAICaller) Call(ctx context.Context, input string) (string, error) {
	return strings.ToUpper(input), nil
}

// FlakyOpenAICaller is an implementation of OpenaiCaller that fails on first call but succeeds on retry
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

// MockEmbeddingsCaller is a simple implementation of OpenaiCaller for embeddings
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

// FlakyEmbeddingsCaller is an implementation of OpenaiCaller for embeddings that fails on first call
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
