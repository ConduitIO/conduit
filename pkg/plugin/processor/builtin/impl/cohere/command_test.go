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

func TestCommandProcessor_Configure(t *testing.T) {
	tests := []struct {
		name    string
		config  config.Config
		wantErr string
	}{
		{
			name: "empty config returns error",
			config: config.Config{
				"prompt": "some preset text",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "apiKey": required parameter is not provided`,
		},
		{
			name: "invalid backoffRetry.count returns error",
			config: config.Config{
				"apiKey":             "api-key",
				"prompt":             "some preset text",
				"model":              "command",
				"backoffRetry.count": "not-a-number",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "backoffRetry.count": "not-a-number" value is not a float: invalid parameter type`,
		},
		{
			name: "invalid backoffRetry.min returns error",
			config: config.Config{
				"apiKey":             "api-key",
				"prompt":             "some preset text",
				"model":              "command",
				"backoffRetry.count": "1",
				"backoffRetry.min":   "not-a-duration",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "backoffRetry.min": "not-a-duration" value is not a duration: invalid parameter type`,
		},
		{
			name: "invalid backoffRetry.max returns error",
			config: config.Config{
				"apiKey":             "api-key",
				"prompt":             "some preset text",
				"model":              "command",
				"backoffRetry.count": "1",
				"backoffRetry.max":   "not-a-duration",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "backoffRetry.max": "not-a-duration" value is not a duration: invalid parameter type`,
		},
		{
			name: "invalid backoffRetry.factor returns error",
			config: config.Config{
				"apiKey":              "api-key",
				"prompt":              "some preset text",
				"model":               "command",
				"backoffRetry.count":  "1",
				"backoffRetry.factor": "not-a-number",
			},
			wantErr: `failed to parse configuration: config invalid: error validating "backoffRetry.factor": "not-a-number" value is not a float: invalid parameter type`,
		},
		{
			name: "success",
			config: config.Config{
				"apiKey": "api-key",
				"prompt": "some preset text",
			},
			wantErr: ``,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			p := NewCommandProcessor(log.Test(t))
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

func TestCommandProcessor_Process(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	t.Run("successful processing", func(t *testing.T) {
		p := func() sdk.Processor {
			proc := &commandProcessor{}
			cfg := config.Config{
				commandProcessorConfigApiKey: "apikey",
				commandProcessorConfigPrompt: "test-prompt",
			}
			is.NoErr(proc.Configure(ctx, cfg))
			proc.client = &mockCommandClient{}
			return proc
		}()

		records := []opencdc.Record{{
			Operation: opencdc.OperationUpdate,
			Position:  opencdc.Position("pos-1"),
			Payload: opencdc.Change{
				After: opencdc.RawData("who are you?"),
			},
		}}
		got := p.Process(ctx, records)
		is.Equal(len(got), len(records))
		rec, ok := got[0].(sdk.SingleRecord)
		is.Equal(ok, true)
		is.Equal(string(rec.Payload.After.Bytes()), "cohere command response content")
	})
}
