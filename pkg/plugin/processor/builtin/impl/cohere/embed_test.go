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
	"github.com/conduitio/conduit/pkg/foundation/log"
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
