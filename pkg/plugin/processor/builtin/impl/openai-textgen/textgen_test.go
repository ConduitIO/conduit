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

package textgen

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/matryer/is"
	"github.com/sashabaranov/go-openai"
)

func TestProcessor_Process(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	processor := newProcessor(ctx, is,
		"You will receive a payload. Your task is to output back the payload in uppercase.")

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

func newProcessor(ctx context.Context, is *is.I, devMessage string) sdk.Processor {
	processor := &Processor{}

	cfg := config.Config{
		ProcessorConfigModel:            openai.GPT4oMini,
		ProcessorConfigApiKey:           "fake api key",
		ProcessorConfigDeveloperMessage: devMessage,
		ProcessorConfigTemperature:      "0",
	}

	is.NoErr(processor.Configure(ctx, cfg))
	processor.call = fakeOpenaiCall{}

	return processor
}

func testRecords() []opencdc.Record {
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

type fakeOpenaiCall struct{}

func (f fakeOpenaiCall) Call(ctx context.Context, input string) (string, error) {
	return strings.ToUpper(input), nil
}
