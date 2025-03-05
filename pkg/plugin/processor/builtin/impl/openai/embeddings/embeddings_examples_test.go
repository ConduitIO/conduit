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

package embeddings

import (
	"context"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

type mockOpenAICallForExamples struct{}

var (
	testVector  = []float32{0.1, 0.2, 0.3, 0.4, 0.5}
	testVectorS = "[0.1,0.2,0.3,0.4,0.5]"
)

func (m *mockOpenAICallForExamples) Call(ctx context.Context, input string) ([]float32, error) {
	return testVector, nil
}

//nolint:govet // we're using a more descriptive name of example
func ExampleEmbeddingsProcessor() {
	processor := &embeddingsProcessor{}
	processor.call = &mockOpenAICallForExamples{}

	exampleutil.RunExample(processor, exampleutil.Example{
		Summary: "Generate embeddings for text",
		Description: `This example generates embeddings for the text stored in
` + "`.Payload.After`" + `. The embeddings are returned as a JSON array of floating point numbers.
These embeddings can be used for semantic search, clustering, or other machine learning tasks.`,
		Config: config.Config{
			"api_key": "your-openai-api-key",
			"model":   "text-embedding-3-small",
			"field":   ".Payload.After",
		},
		Have: opencdc.Record{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.RawData("test-key"),
			Payload: opencdc.Change{
				After: opencdc.RawData("This is a sample text to generate embeddings for."),
			},
		},
		Want: sdk.SingleRecord{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.RawData("test-key"),
			Payload: opencdc.Change{
				After: opencdc.RawData(testVectorS),
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,12 +1,12 @@
	//  {
	//    "position": "dGVzdC1wb3NpdGlvbg==",
	//    "operation": "create",
	//    "metadata": {
	//      "key1": "val1"
	//    },
	//    "key": "test-key",
	//    "payload": {
	//      "before": null,
	// -    "after": "This is a sample text to generate embeddings for."
	// +    "after": "[0.1,0.2,0.3,0.4,0.5]"
	//    }
	//  }
}
