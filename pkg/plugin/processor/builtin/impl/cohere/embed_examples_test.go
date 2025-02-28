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
)

//nolint:govet // a more descriptive example description
func ExampleEmbedProcessor() {
	ctx := context.Background()

	p := func() sdk.Processor {
		proc := &embedProcessor{}
		cfg := config.Config{
			embedProcConfigApiKey: "apikey",
		}
		_ = proc.Configure(ctx, cfg)
		_ = proc.Open(ctx)
		proc.client = &mockEmbedClient{}
		return proc
	}()

	records := []opencdc.Record{{
		Operation: opencdc.OperationUpdate,
		Position:  opencdc.Position("pos-1"),
		Payload: opencdc.Change{
			After: opencdc.RawData("test input"),
		},
		Metadata: map[string]string{},
	}}

	got := p.Process(context.Background(), records)
	rec, _ := got[0].(sdk.SingleRecord)
	fmt.Println("processor transformed record:")
	fmt.Println(string(opencdc.Record(rec).Bytes()))

	// Output:
	// processor transformed record:
	// {"position":"cG9zLTE=","operation":"update","metadata":{"cohere.embed.model":"embed-english-v2.0"},"key":null,"payload":{"before":null,"after":"KLUv/QQAaQAAWzAuMSwwLjIsMC4zXYleeEg="}}
}
