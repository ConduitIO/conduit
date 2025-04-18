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
	"math"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

func ExampleRerankProcessor() {
	p := NewRerankProcessor(log.Nop())
	p.client = &mockRerankClient{}

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Generate responses using Cohere's rerank model`,
		Description: `
This example demonstrates how to use the Cohere rerank processor.This takes in a query and a list of texts and produces an ordered 
array with each text assigned a relevance score. The processor extracts text from the configured "request.body" (default: ".Payload.After"), 
sends it to the Cohere API, and stores the response in the configured "response.body".

In this example, the processor is configured with a mock client and an API key. The input record's metadata is updated
to include the rerank model used ("rerank-v3.5").`,
		Config: config.Config{
			"apiKey": "fakeapiKey",
			"query":  "What is the capital of the United States?",
		},
		Have: opencdc.Record{
			Operation: opencdc.OperationUpdate,
			Position:  opencdc.Position("pos-1"),
			Payload: opencdc.Change{
				After: opencdc.RawData("Carson City is the capital city of the American state of Nevada."),
			},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationUpdate,
			Position:  opencdc.Position("pos-1"),
			Payload: opencdc.Change{
				After: opencdc.RawData(`{"document":{"text":"Carson City is the capital city of the American state of Nevada."},"index":0,"relevance_score":0.9}`),
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
	// -    "after": "Carson City is the capital city of the American state of Nevada."
	// +    "after": "{\"document\":{\"text\":\"Carson City is the capital city of the American state of Nevada.\"},\"index\":0,\"relevance_score\":0.9}"
	//    }
	//  }
}

type mockRerankClient struct{}

func (m mockRerankClient) rerank(ctx context.Context, docs []string) ([]RerankResult, error) {
	if len(docs) == 0 {
		return nil, fmt.Errorf("mocked api error")
	}
	result := make([]RerankResult, 0, len(docs))

	mockedScore := 1.0
	for i, d := range docs {
		mockedScore -= 0.1
		mockedScore = math.Round(mockedScore*10) / 10
		res := RerankResult{
			Index:          i,
			RelevanceScore: mockedScore,
		}
		res.Document.Text = d
		result = append(result, res)
	}

	return result, nil
}
