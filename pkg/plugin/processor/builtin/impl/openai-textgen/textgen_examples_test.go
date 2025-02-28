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
	conduit_log "github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
	"github.com/sashabaranov/go-openai"
)

// mockOpenAICall is a mock implementation of the openaiCall interface for testing
type mockOpenAICall struct{}

func (m *mockOpenAICall) Call(_ context.Context, input string) (string, error) {
	if strings.Contains(input, "hello world") {
		return strings.ToUpper(input), nil
	}

	if strings.Contains(input, "team discussed") {
		return "The team prioritized user authentication for the next release, postponed UI redesign, and agreed on the importance of performance improvements.", nil
	}

	return strings.ToUpper(input), nil
}

// mockTextgenProcessor creates a processor with a mock OpenAI call implementation
func mockTextgenProcessor(l conduit_log.CtxLogger) sdk.Processor {
	p := NewTextgenProcessor(l)

	if tp, ok := p.(*textgenProcessor); ok {
		tp.call = &mockOpenAICall{}
	}

	return p
}

func ExampletextgenProcessor() {
	p := mockTextgenProcessor(conduit_log.Nop())

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

//nolint:govet // we're using a more descriptive name of example
func ExampleTextgenProcessor_CustomField() {
	p := mockTextgenProcessor(conduit_log.Nop())

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Transform a specific field in a structured record`,
		Description: `
This example shows how to use the OpenAI text generation processor to transform a specific field in a structured record.
The processor will send the content of the specified field to OpenAI and replace it with the response.

In this example, we're targeting the ` + "`.Payload.After.description`" + ` field and using a system message that instructs
the model to summarize the text.`,
		Config: config.Config{
			"api_key":           "fake-api-key",
			"model":             openai.GPT4oMini,
			"field":             ".Payload.After.description",
			"developer_message": "Summarize the following text in one sentence.",
			"temperature":       "0",
		},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Position:  opencdc.Position("pos-1"),
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"title":       "Meeting Notes",
					"description": "The team discussed the new product features. We decided to prioritize the user authentication system. The UI redesign will be postponed until next quarter. Everyone agreed that improving performance is critical for the next release.",
				},
			},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Position:  opencdc.Position("pos-1"),
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"title":       "Meeting Notes",
					"description": "The team prioritized user authentication for the next release, postponed UI redesign, and agreed on the importance of performance improvements.",
				},
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,13 +1,13 @@
	//  {
	//    "position": "cG9zLTE=",
	//    "operation": "create",
	//    "metadata": null,
	//    "key": null,
	//    "payload": {
	//      "before": null,
	//      "after": {
	// -      "description": "The team discussed the new product features. We decided to prioritize the user authentication system. The UI redesign will be postponed until next quarter. Everyone agreed that improving performance is critical for the next release.",
	// +      "description": "The team prioritized user authentication for the next release, postponed UI redesign, and agreed on the importance of performance improvements.",
	//        "title": "Meeting Notes"
	//      }
	//    }
	//  }
}
