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
	"log"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
	"github.com/goccy/go-json"
)

//nolint:govet // a more descriptive example description
func ExampleEmbedProcessor() {
	p := &embedProcessor{}
	p.client = mockEmbedClient{}

	embedding, err := p.client.embed(context.Background(), []string{"test input"})
	if err != nil {
		log.Fatal("failed to get embedding")
	}
	if len(embedding) == 0 {
		log.Fatal("no embeddings found")
	}

	embeddingJSON, err := json.Marshal(embedding[0])
	if err != nil {
		log.Fatalf("failed to marshal embeddings: %v", err)
	}

	// Compress the embedding using zstd
	compressedEmbedding, err := compressData(embeddingJSON)
	if err != nil {
		log.Fatalf("failed to compress embeddings: %v", err)
	}

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Generate embeddings using Cohere's embedding model`,
		Description: `
This example demonstrates how to use the Cohere embedding processor to generate embeddings for a record.
The processor extracts text from the configured "inputField" (default: ".Payload.After"), sends it to the Cohere API,
and stores the resulting embeddings in the configured "outputField" as compressed data using the zstd algorithm.

In this example, the processor is configured with a mock client and an API key. The input record's metadata is updated
to include the embedding model used ("embed-english-v2.0").`,
		Config: config.Config{
			"apiKey":      "fake-api-key",
			"inputField":  ".Payload.After",
			"outputField": ".Payload.After",
		},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Position:  opencdc.Position("pos-1"),
			Metadata:  map[string]string{},
			Payload: opencdc.Change{
				After: opencdc.RawData("test input"),
			},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Position:  opencdc.Position("pos-1"),
			Metadata:  opencdc.Metadata{"cohere.embed.model": "embed-english-v2.0"},
			Payload: opencdc.Change{
				After: opencdc.RawData(compressedEmbedding),
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,10 +1,12 @@
	//  {
	//    "position": "cG9zLTE=",
	//    "operation": "create",
	// -  "metadata": {},
	// +  "metadata": {
	// +    "cohere.embed.model": "embed-english-v2.0"
	// +  },
	//    "key": null,
	//    "payload": {
	//      "before": null,
	// -    "after": "test input"
	// +    "after": "(\ufffd/\ufffd\u0004\u0000i\u0000\u0000[0.1,0.2,0.3]\ufffd^xH"
	//    }
	//  }
}
