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
	"github.com/matryer/is"
)

func TestRerankProcessor_Configure(t *testing.T) {
	tests := []struct {
		name    string
		config  config.Config
		wantErr string
	}{
		{
			name: "empty config returns error",
			config: config.Config{
				"query": "some query text",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "apiKey": required parameter is not provided`,
		},
		{
			name: "invalid backoffRetry.count returns error",
			config: config.Config{
				"apiKey":             "api-key",
				"query":              "some query text",
				"backoffRetry.count": "not-a-number",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "backoffRetry.count": "not-a-number" value is not a float: invalid parameter type`,
		},
		{
			name: "invalid backoffRetry.min returns error",
			config: config.Config{
				"apiKey":             "api-key",
				"query":              "some query text",
				"backoffRetry.count": "1",
				"backoffRetry.min":   "not-a-duration",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "backoffRetry.min": "not-a-duration" value is not a duration: invalid parameter type`,
		},
		{
			name: "invalid backoffRetry.max returns error",
			config: config.Config{
				"apiKey":             "api-key",
				"query":              "some query text",
				"backoffRetry.count": "1",
				"backoffRetry.max":   "not-a-duration",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "backoffRetry.max": "not-a-duration" value is not a duration: invalid parameter type`,
		},
		{
			name: "invalid backoffRetry.factor returns error",
			config: config.Config{
				"apiKey":              "api-key",
				"query":               "some query text",
				"backoffRetry.count":  "1",
				"backoffRetry.factor": "not-a-number",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "backoffRetry.factor": "not-a-number" value is not a float: invalid parameter type`,
		},
		{
			name: "success",
			config: config.Config{
				"apiKey": "api-key",
				"query":  "some query text",
			},
			wantErr: ``,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			p := NewRerankProcessor(log.Test(t))
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

func TestRerankProcessor_Process(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := log.Nop()

	t.Run("successful processing", func(t *testing.T) {
		p := func() sdk.Processor {
			proc := NewRerankProcessor(logger)
			cfg := config.Config{
				rerankConfigApiKey: "apikey",
				rerankConfigQuery:  "What is the capital of the United States?",
			}
			is.NoErr(proc.Configure(ctx, cfg))
			proc.client = &mockRerankClient{}
			return proc
		}()

		records := []opencdc.Record{
			{
				Payload: opencdc.Change{
					After: opencdc.RawData("Washington, D.C. is the capital of the United States."),
				},
			},
			{
				Payload: opencdc.Change{
					After: opencdc.RawData("Carson City is the capital city of the American state of Nevada."),
				},
			},
			{
				Payload: opencdc.Change{
					After: opencdc.RawData("The Commonwealth of the Northern Mariana Islands is a group of islands in the Pacific Ocean. Its capital is Saipan."),
				},
			},
		}
		got := p.Process(ctx, records)
		is.Equal(len(got), len(records))

		want := []opencdc.Record{
			{
				Payload: opencdc.Change{
					After: opencdc.RawData(`{"document":{"text":"Washington, D.C. is the capital of the United States."},"index":0,"relevance_score":0.9}`),
				},
			},
			{
				Payload: opencdc.Change{
					After: opencdc.RawData(`{"document":{"text":"Carson City is the capital city of the American state of Nevada."},"index":1,"relevance_score":0.8}`),
				},
			},
			{
				Payload: opencdc.Change{
					After: opencdc.RawData(`{"document":{"text":"The Commonwealth of the Northern Mariana Islands is a group of islands in the Pacific Ocean. Its capital is Saipan."},"index":2,"relevance_score":0.7}`),
				},
			},
		}

		for i, v := range got {
			val, ok := v.(sdk.SingleRecord)
			is.Equal(ok, true)
			is.Equal(val.Payload.After.Bytes(), want[i].Payload.After.Bytes())
		}
	})
}
