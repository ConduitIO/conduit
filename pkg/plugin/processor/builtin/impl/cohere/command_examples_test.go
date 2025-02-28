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

func ExampleCommandProcessor() {
	p := func() sdk.Processor {
		proc := &commandProcessor{}
		cfg := config.Config{
			commandProcessorConfigApiKey: "apikey",
			commandProcessorConfigPrompt: "hello",
		}
		proc.Configure(context.Background(), cfg)
		proc.client = &mockClient{}
		return proc
	}()

	records := []opencdc.Record{{
		Operation: opencdc.OperationUpdate,
		Position:  opencdc.Position("pos-1"),
		Payload: opencdc.Change{
			After: opencdc.RawData("who are you?"),
		},
	}}

	got := p.Process(context.Background(), records)
	rec, _ := got[0].(sdk.SingleRecord)
	fmt.Println("processor transformed record:")
	fmt.Println(string(opencdc.Record(rec).Bytes()))

	// Output:
	// processor transformed record:
	// {"position":"cG9zLTE=","operation":"update","metadata":null,"key":null,"payload":{"before":null,"after":"Y29oZXJlIGNvbW1hbmQgcmVzcG9uc2UgY29udGVudA=="}}
}

type mockClient struct{}

func (m mockClient) Command(ctx context.Context, content string) (string, error) {
	if content == "" {
		return "", fmt.Errorf("mocked api error")
	}
	return "cohere command response content", nil
}
