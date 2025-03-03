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
	"strings"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
	"github.com/sashabaranov/go-openai"
)

//nolint:govet // we're using a more descriptive name of example
func ExampleTextgenProcessor() {
	p := &textgenProcessor{}
	p.call = &mockOpenAICall{}

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Transform text using OpenAI models`,
		Description: `
This example shows how to use the OpenAI text generation processor to transform a record's ` + "`.Payload.After`" + ` field
using an OpenAI model. The processor will send the content of the field to OpenAI and replace it with the response.

In this example, we're using a system message that instructs the model to convert the input text to uppercase.`,
		Config: config.Config{
			"api_key":           "fake-api-key",
			"model":             openai.GPT4oMini,
			"developer_message": "You will receive a payload. Your task is to output back the payload in uppercase.",
			"temperature":       "0",
		},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Position:  opencdc.Position("pos-1"),
			Payload: opencdc.Change{
				After: opencdc.RawData("hello world"),
			},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Position:  opencdc.Position("pos-1"),
			Payload: opencdc.Change{
				After: opencdc.RawData("HELLO WORLD"),
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,10 +1,10 @@
	//  {
	//    "position": "cG9zLTE=",
	//    "operation": "create",
	//    "metadata": null,
	//    "key": null,
	//    "payload": {
	//      "before": null,
	// -    "after": "hello world"
	// +    "after": "HELLO WORLD"
	//    }
	//  }
}

type mockOpenAICall struct{}

func (m *mockOpenAICall) Call(_ context.Context, input string) (string, error) {
	return strings.ToUpper(input), nil
}
