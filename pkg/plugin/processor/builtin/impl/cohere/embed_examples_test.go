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
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

func ExampleEmbedProcessor() {
	p := NewEmbedProcessor(log.Nop())
	p.client = mockEmbedClient{}

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Generate embeddings using Cohere's embedding model`,
		Description: `
This example demonstrates how to use the Cohere embedding processor to generate embeddings for a record.
The processor extracts text from the specified input field (default: ".Payload.After"), sends it to the Cohere API,
and stores the resulting embeddings in the record's ".Payload.After" field as compressed data using the zstd algorithm.

In this example, the processor is configured with a mock client and an API key. The input record's metadata is updated
to include the embedding model used ("embed-english-v2.0"). Note that the compressed embeddings cannot be directly compared
in this test, so the focus is on verifying the metadata update.`,
		Config: config.Config{
			"apiKey": "fake-api-key",
		},
		Have: opencdc.Record{
			Operation: opencdc.OperationCreate,
			Position:  opencdc.Position("pos-1"),
			Metadata:  map[string]string{},
		},
		Want: sdk.SingleRecord{
			Operation: opencdc.OperationCreate,
			Position:  opencdc.Position("pos-1"),
			Metadata:  opencdc.Metadata{"cohere.embed.model": "embed-english-v2.0"},
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
	//      "after": null
	//    }
	//  }
}
