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

package ollama

import (
	"context"
	_ "embed"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/ollama/mock"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

//go:embed test/ollama-response.json
var ollamaResp string

const ollamaURL = "http://localhost:11434"

func TestOllamaProcessor_Configure(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name    string
		config  config.Config
		wantErr error
	}{
		{
			name: "applies set params",
			config: config.Config{
				ollamaProcessorConfigModel:  "llama3.2",
				ollamaProcessorConfigPrompt: "hello world",
				ollamaProcessorConfigUrl:    ollamaURL,
				ollamaProcessorConfigField:  ".Payload.After",
			},
		},
		{
			name:    "errors with no required param",
			config:  config.Config{},
			wantErr: cerrors.New("failed to parse configuration: config invalid: error validating \"url\": required parameter is not provided"),
		},
		{
			name: "invalid field pass through",
			config: config.Config{
				ollamaProcessorConfigUrl:   ollamaURL,
				ollamaProcessorConfigField: ".Payload.Atfer",
			},
			wantErr: cerrors.New("failed to create reference resolver: invalid reference \".Payload.Atfer\": unexpected field \"Atfer\": cannot resolve reference"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			p := &ollamaProcessor{}
			cfg := tc.config

			err := p.Configure(ctx, cfg)

			if tc.wantErr == nil {
				is.NoErr(err)
			} else {
				is.True(err != nil)
				is.Equal(tc.wantErr.Error(), err.Error())
			}
		})
	}
}

//nolint:bodyclose // This is a false positive, the close occurs in the ollama file
func TestOllamaProcessor_Success(t *testing.T) {
	ctx := context.Background()
	ollamaURL := "http://localhost:11434"

	testCases := []struct {
		name    string
		setup   func() config.Config
		records []opencdc.Record
		want    []sdk.ProcessedRecord
	}{
		{
			name: "successfully processes record",
			setup: func() config.Config {
				client := setupHTTPMockClient(t)
				resp := getOllamaResp(t)
				client.EXPECT().Do(gomock.Any()).DoAndReturn(resp)

				return config.Config{
					ollamaProcessorConfigUrl: ollamaURL,
				}
			},
			records: []opencdc.Record{
				{
					Payload: opencdc.Change{
						After: opencdc.StructuredData{
							"foo": "bar",
						},
					},
				},
			},
			want: []sdk.ProcessedRecord{
				sdk.SingleRecord{
					Payload: opencdc.Change{
						After: opencdc.StructuredData{
							"hello": "world",
						},
					},
				},
			},
		},
		{
			name: "uses custom field",
			setup: func() config.Config {
				client := setupHTTPMockClient(t)
				resp := getOllamaResp(t)
				client.EXPECT().Do(gomock.Any()).DoAndReturn(resp)

				return config.Config{
					ollamaProcessorConfigModel:  "llama3.2",
					ollamaProcessorConfigField:  ".Payload.Before",
					ollamaProcessorConfigPrompt: "hello world",
					ollamaProcessorConfigUrl:    "http://localhost:11434",
				}
			},
			records: []opencdc.Record{
				{
					Payload: opencdc.Change{
						Before: opencdc.StructuredData{
							"test1": "test2",
						},
						After: opencdc.StructuredData{
							"foo": "bar",
						},
					},
				},
			},
			want: []sdk.ProcessedRecord{
				sdk.SingleRecord{
					Payload: opencdc.Change{
						Before: opencdc.StructuredData{
							"hello": "world",
						},
						After: opencdc.StructuredData{
							"foo": "bar",
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			config := tc.setup()
			p := NewOllamaProcessor(log.Nop())
			err := p.Configure(ctx, config)
			is.NoErr(err)

			got := p.Process(ctx, tc.records)
			is.Equal(len(tc.records), len(got))

			for i, rec := range got {
				_, isError := rec.(sdk.ErrorRecord)
				is.Equal(isError, false)

				processed, ok := rec.(sdk.SingleRecord)
				is.True(ok)

				is.Equal(processed, tc.want[i])
			}
		})
	}
}

func TestOllamaProcessor_Failure(t *testing.T) {
	ctx := context.Background()
	ollamaURL := "http://localhost:11434"

	testcases := []struct {
		name    string
		setup   func() config.Config
		records []opencdc.Record
		wantErr error
	}{
		{
			name: "error occurs from ollama connection",
			setup: func() config.Config {
				client := setupHTTPMockClient(t)
				respFn := func(_ *http.Request) (*http.Response, error) {
					resp := &http.Response{
						StatusCode: 500,
						Body:       io.NopCloser(strings.NewReader("Couldn't connect")),
					}
					return resp, nil
				}
				client.EXPECT().Do(gomock.Any()).DoAndReturn(respFn)

				return config.Config{
					ollamaProcessorConfigUrl: ollamaURL,
				}
			},
			records: []opencdc.Record{
				{
					Payload: opencdc.Change{
						After: opencdc.StructuredData{
							"foo": "bar",
						},
					},
				},
			},
			wantErr: cerrors.New("error sending the ollama request ollama request returned status code 500"),
		},
		{
			name: "invalid model pass through",
			setup: func() config.Config {
				return config.Config{
					ollamaProcessorConfigUrl:   ollamaURL,
					ollamaProcessorConfigModel: "chatgpt",
				}
			},
			records: []opencdc.Record{
				{
					Payload: opencdc.Change{
						After: opencdc.StructuredData{
							"foo": "bar",
						},
					},
				},
			},
			wantErr: cerrors.New("model {chatgpt} not allowed by processor"),
		},
		{
			name: "bad format returned from ollama",
			setup: func() config.Config {
				client := setupHTTPMockClient(t)
				respFn := func(_ *http.Request) (*http.Response, error) {
					resp := &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(strings.NewReader("hello world")),
					}
					return resp, nil
				}
				client.EXPECT().Do(gomock.Any()).DoAndReturn(respFn)

				return config.Config{
					ollamaProcessorConfigUrl: ollamaURL,
				}
			},
			records: []opencdc.Record{
				{
					Payload: opencdc.Change{
						After: opencdc.StructuredData{
							"foo": "bar",
						},
					},
				},
			},
			wantErr: cerrors.New("cannot process response: invalid JSON in ollama response: invalid character 'h' looking for beginning of value"),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			config := tc.setup()
			p := NewOllamaProcessor(log.Nop())
			err := p.Configure(ctx, config)
			is.NoErr(err)

			result := p.Process(ctx, tc.records)

			for _, rec := range result {
				is.Equal("", cmp.Diff(sdk.ErrorRecord{Error: tc.wantErr}, rec, internal.CmpProcessedRecordOpts...))
			}
		})
	}
}

func setupHTTPMockClient(t *testing.T) *mock.MockhttpClient {
	t.Helper()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock.NewMockhttpClient(ctrl)
	HTTPClient = mockClient

	return mockClient
}

func getOllamaResp(t *testing.T) func(*http.Request) (*http.Response, error) {
	t.Helper()

	respFn := func(_ *http.Request) (*http.Response, error) {
		resp := &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(ollamaResp)),
		}
		return resp, nil
	}

	return respFn
}
