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

package ollama

import (
	_ "embed"
	"io"
	"net/http"
	"strings"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

//go:embed test/ollama-record-response.json
var ollamaRecResp string

func ExampleOllamaProcessor() {
	processor := NewOllamaProcessor(log.Nop())

	mockClient := &MockhttpClient{
		DoFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(ollamaRecResp)),
			}, nil
		},
	}
	HTTPClient = mockClient

	exampleutil.RunExample(processor, exampleutil.Example{
		Summary: "Ollama Prompt Example",
		Description: `This example will process the record stored in
` + "`.Payload.After`" + `. The model is prompted to return the records unchanged. 
The records are returned as a JSON with the same format as the data. `,
		Config: config.Config{
			"prompt": "Take the incoming record in JSON format, with a structure of {'test-field': integer}. Add one to the value of the integer and return that field.",
			"model":  "llama3.2",
			"url":    "http://localhost:11434",
			"field":  ".Payload.After",
		},
		Have: opencdc.Record{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.RawData("test-key"),
			Payload: opencdc.Change{
				After: opencdc.StructuredData{"test-field": 123},
			},
		},
		Want: sdk.SingleRecord{
			Position:  opencdc.Position("test-position"),
			Operation: opencdc.OperationCreate,
			Metadata:  map[string]string{"key1": "val1"},
			Key:       opencdc.RawData("test-key"),
			Payload: opencdc.Change{
				After: opencdc.StructuredData{"test-field": float64(124)},
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,14 +1,14 @@
	//  {
	//    "position": "dGVzdC1wb3NpdGlvbg==",
	//    "operation": "create",
	//    "metadata": {
	//      "key1": "val1"
	//    },
	//    "key": "test-key",
	//    "payload": {
	//      "before": null,
	//      "after": {
	// -      "test-field": 123
	// +      "test-field": 124
	//      }
	//    }
	//  }
}

type MockhttpClient struct {
	DoFunc func(req *http.Request) (*http.Response, error)
}

func (m *MockhttpClient) Do(req *http.Request) (*http.Response, error) {
	if m.DoFunc != nil {
		return m.DoFunc(req)
	}
	return &http.Response{StatusCode: 200}, nil
}
