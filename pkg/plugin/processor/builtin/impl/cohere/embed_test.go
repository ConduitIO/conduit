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
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

func TestEmbedProcessor_Configure(t *testing.T) {
	tests := []struct {
		name    string
		config  config.Config
		wantErr string
	}{
		{
			name: "empty config returns error",
			config: config.Config{
				"inputType": "search_document",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "apiKey": required parameter is not provided`,
		},
		{
			name: "invalid backoffRetry.count returns error",
			config: config.Config{
				"apiKey":             "api-key",
				"model":              "embed",
				"backoffRetry.count": "not-a-number",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "backoffRetry.count": "not-a-number" value is not a float: invalid parameter type`,
		},
		{
			name: "invalid backoffRetry.min returns error",
			config: config.Config{
				"apiKey":             "api-key",
				"model":              "embed",
				"backoffRetry.count": "1",
				"backoffRetry.min":   "not-a-duration",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "backoffRetry.min": "not-a-duration" value is not a duration: invalid parameter type`,
		},
		{
			name: "invalid backoffRetry.max returns error",
			config: config.Config{
				"apiKey":             "api-key",
				"model":              "embed",
				"backoffRetry.count": "1",
				"backoffRetry.max":   "not-a-duration",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "backoffRetry.max": "not-a-duration" value is not a duration: invalid parameter type`,
		},
		{
			name: "invalid backoffRetry.factor returns error",
			config: config.Config{
				"apiKey":              "api-key",
				"model":               "embed",
				"backoffRetry.count":  "1",
				"backoffRetry.factor": "not-a-number",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "backoffRetry.factor": "not-a-number" value is not a float: invalid parameter type`,
		},
		{
			name: "missing inputType for v3 model returns error",
			config: config.Config{
				"apiKey": "api-key",
				"model":  "embed-english-light-v3.0",
			},
			wantErr: `error validating configuration: inputType is required for model "embed-english-light-v3.0" (v3 or higher)`,
		},
		{
			name: "success",
			config: config.Config{
				"apiKey":    "api-key",
				"inputType": "search_document",
			},
			wantErr: ``,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			p := NewEmbedProcessor(log.Test(t))
			err := p.Configure(context.Background(), tc.config)
			if tc.wantErr == "" {
				is.NoErr(err)
			} else {
				is.True(err != nil)
				is.Equal(tc.wantErr, err.Error())
			}
		})
	}
}

func TestEmbedProcessor_Open(t *testing.T) {
	tests := []struct {
		name    string
		config  config.Config
		wantErr string
	}{
		{
			name: "successful open",
			config: config.Config{
				"apiKey":     "api-key",
				"inputField": ".Payload.After",
			},
			wantErr: "",
		},
		{
			name: "invalid input field",
			config: config.Config{
				"apiKey":     "api-key",
				"inputField": ".Payload.Invalid",
			},
			wantErr: `failed to create a field resolver for .Payload.Invalid parameter: invalid reference ".Payload.Invalid": unexpected field "Invalid": cannot resolve reference`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			p := NewEmbedProcessor(log.Test(t))
			err := p.Configure(context.Background(), tc.config)
			is.NoErr(err)

			err = p.Open(context.Background())
			if tc.wantErr == "" {
				is.NoErr(err)
			} else {
				is.True(err != nil)
				is.Equal(tc.wantErr, err.Error())
			}
		})
	}
}

func TestEmbedProcessor_Process(t *testing.T) {
	tests := []struct {
		name    string
		config  config.Config
		records []opencdc.Record
		want    []sdk.ProcessedRecord
		wantErr string
	}{
		{
			name: "successful process single record to replace .Payload.After with result of the request",
			config: config.Config{
				embedProcConfigApiKey:      "api-key",
				embedProcConfigOutputField: ".Payload.After",
			},
			records: []opencdc.Record{
				{
					Payload: opencdc.Change{
						After: opencdc.RawData("test input"),
					},
					Metadata: map[string]string{},
				},
			},
			want: func() []sdk.ProcessedRecord {
				embeddingJSON, err := json.Marshal([]float64{0.1, 0.2, 0.3})
				if err != nil {
					t.Error(err)
				}

				// Compress the embedding using zstd
				compressedEmbedding, err := compressData(embeddingJSON)
				if err != nil {
					t.Error(err)
				}

				result := []sdk.ProcessedRecord{
					sdk.SingleRecord(opencdc.Record{
						Payload: opencdc.Change{
							After: opencdc.RawData(compressedEmbedding),
						},
						Metadata: map[string]string{
							EmbedModelMetadata: "embed-english-v2.0",
						},
					}),
				}
				return result
			}(),
			wantErr: "",
		},
		{
			name: "failed to process single record to set new field 'response' in .Payload.After having raw data",
			config: config.Config{
				embedProcConfigApiKey:      "api-key",
				embedProcConfigOutputField: ".Payload.After.response",
			},
			records: []opencdc.Record{
				{
					Payload: opencdc.Change{
						After: opencdc.RawData("test input"),
					},
					Metadata: map[string]string{},
				},
			},
			wantErr: `failed to set output: error reference resolver: could not resolve field "response": .Payload.After does not contain structured data: cannot resolve reference`,
		},
		{
			name: "successful process single record to set new field 'response' in .Payload.After having structured data",
			config: config.Config{
				embedProcConfigApiKey:      "api-key",
				embedProcConfigOutputField: ".Payload.After.response",
			},
			records: []opencdc.Record{
				{
					Payload: opencdc.Change{
						After: opencdc.StructuredData{"test": "testInput"},
					},
					Metadata: map[string]string{},
				},
			},
			want: func() []sdk.ProcessedRecord {
				embeddingJSON, err := json.Marshal([]float64{0.1, 0.2, 0.3})
				if err != nil {
					t.Error(err)
				}

				// Compress the embedding using zstd
				compressedEmbedding, err := compressData(embeddingJSON)
				if err != nil {
					t.Error(err)
				}

				result := []sdk.ProcessedRecord{
					sdk.SingleRecord(opencdc.Record{
						Payload: opencdc.Change{
							After: opencdc.StructuredData{"test": "testInput", "response": compressedEmbedding},
						},
						Metadata: map[string]string{
							EmbedModelMetadata: "embed-english-v2.0",
						},
					}),
				}
				return result
			}(),
			wantErr: "",
		},
		{
			name: "batch processing with multiple records",
			config: config.Config{
				embedProcConfigApiKey:             "api-key",
				embedProcConfigMaxTextsPerRequest: "2",
			},
			records: []opencdc.Record{
				{
					Payload: opencdc.Change{
						After: opencdc.RawData("test input 1"),
					},
					Metadata: map[string]string{},
				},
				{
					Payload: opencdc.Change{
						After: opencdc.RawData("test input 2"),
					},
					Metadata: map[string]string{},
				},
				{
					Payload: opencdc.Change{
						After: opencdc.RawData("test input 3"),
					},
					Metadata: map[string]string{},
				},
			},
			want: func() []sdk.ProcessedRecord {
				embeddingJSON, err := json.Marshal([]float64{0.1, 0.2, 0.3})
				if err != nil {
					t.Error(err)
				}

				// Compress the embedding using zstd
				compressedEmbedding, err := compressData(embeddingJSON)
				if err != nil {
					t.Error(err)
				}

				result := []sdk.ProcessedRecord{
					sdk.SingleRecord(opencdc.Record{
						Payload: opencdc.Change{
							After: opencdc.RawData(compressedEmbedding),
						},
						Metadata: map[string]string{
							EmbedModelMetadata: "embed-english-v2.0",
						},
					}),
					sdk.SingleRecord(opencdc.Record{
						Payload: opencdc.Change{
							After: opencdc.RawData(compressedEmbedding),
						},
						Metadata: map[string]string{
							EmbedModelMetadata: "embed-english-v2.0",
						},
					}),
					sdk.SingleRecord(opencdc.Record{
						Payload: opencdc.Change{
							After: opencdc.RawData(compressedEmbedding),
						},
						Metadata: map[string]string{
							EmbedModelMetadata: "embed-english-v2.0",
						},
					}),
				}
				return result
			}(),
			wantErr: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			p := NewEmbedProcessor(log.Nop())

			err := p.Configure(ctx, tc.config)
			is.NoErr(err)

			// Call the Open method to initialize the processor
			err = p.Open(ctx)
			is.NoErr(err)

			// Overwrite the embedding client to produce desired response
			p.client = mockEmbedClient{}

			got := p.Process(ctx, tc.records)

			if tc.wantErr == "" {
				is.NoErr(err)
				is.Equal(tc.want, got)
			} else {
				switch r := got[len(got)-1].(type) {
				case sdk.ErrorRecord:
					is.Equal(tc.wantErr, r.Error.Error())
				default:
					t.Error("expected an error record")
				}
			}
		})
	}
}

type mockEmbedClient struct{}

func (m mockEmbedClient) embed(ctx context.Context, texts []string) ([][]float64, error) {
	embedding := []float64{0.1, 0.2, 0.3}
	output := make([][]float64, len(texts))

	for i := range texts {
		output[i] = embedding
	}

	return output, nil
}
