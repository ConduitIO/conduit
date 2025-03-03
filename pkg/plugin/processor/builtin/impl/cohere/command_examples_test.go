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
	"fmt"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

//nolint:govet // a more descriptive example description
func ExampleCommandProcessor() {
	p := &commandProcessor{}
	p.client = &mockClient{}

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Generate responses using Cohere's command model`,
		Description: `
This example demonstrates how to use the Cohere command processor to generate responses for a record's ` + "`.Payload.After`" + ` field.
The processor sends the input text to the Cohere API and replaces it with the model's response.`,
		Config: config.Config{
			commandProcessorConfigApiKey: "apikey",
			commandProcessorConfigPrompt: "hello",
		},
		Have: opencdc.Record{
			Operation: opencdc.OperationUpdate,
			Position:  opencdc.Position("pos-1"),
			Payload: opencdc.Change{
				After: opencdc.RawData("who are you?"),
			},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationUpdate,
			Position:  opencdc.Position("pos-1"),
			Payload: opencdc.Change{
				After: opencdc.RawData("cohere command response content"),
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
	//    "operation": "update",
	//    "metadata": null,
	//    "key": null,
	//    "payload": {
	//      "before": null,
	// -    "after": "who are you?"
	// +    "after": "cohere command response content"
	//    }
	//  }
}

type mockClient struct{}

func (m mockClient) Command(ctx context.Context, content string) (string, error) {
	if content == "" {
		return "", fmt.Errorf("mocked api error")
	}
	return "cohere command response content", nil
}
