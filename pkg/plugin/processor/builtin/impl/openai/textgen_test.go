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
	"strconv"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/sashabaranov/go-openai"
)

func TestProcessor_Process(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	processor := NewTextgenProcessor(log.Nop())

	cfg := config.Config{
		textgenConfigModel:            openai.GPT4oMini,
		textgenConfigApiKey:           "fake api key",
		textgenConfigDeveloperMessage: "You will receive a payload. Your task is to output back the payload in uppercase.",
		textgenConfigTemperature:      "0",
	}

	is.NoErr(processor.Configure(ctx, cfg))
	processor.call = &mockOpenAICaller{}

	recs := testRecords()

	processed := processor.Process(ctx, recs)
	is.Equal(len(processed), 3)

	for i, p := range processed {
		switch p := p.(type) {
		case sdk.SingleRecord:
			is.Equal(string(p.Payload.After.Bytes()), "AFT-REC-"+strconv.Itoa(i+1))
		case sdk.FilterRecord:
			is.Fail() // Filter Record should not happen
		case sdk.ErrorRecord:
			is.Equal("", p.Error.Error())
			is.Fail() // empty error record should not happen
		}
	}
}

func TestProcessorWithRetry(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	processor := NewTextgenProcessor(log.Nop())

	cfg := config.Config{
		textgenConfigModel:            openai.GPT4oMini,
		textgenConfigApiKey:           "fake api key",
		textgenConfigDeveloperMessage: "Test message",
		textgenConfigMaxRetries:       "3",
		textgenConfigInitialBackoff:   "10",
		textgenConfigMaxBackoff:       "100",
		textgenConfigBackoffFactor:    "2.0",
	}

	is.NoErr(processor.Configure(ctx, cfg))

	retryClient := &flakyOpenAICaller{}
	processor.call = retryClient

	// Use just one record instead of all three, so that it's easier to understand the test.
	recs := testRecords()[:1]
	processor.Process(ctx, recs)

	// We expect 2 calls: 1 initial attempt that fails + 1 retry that succeeds
	is.Equal(retryClient.CallCount, 2)
}
