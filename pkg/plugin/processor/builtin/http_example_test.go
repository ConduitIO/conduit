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

package builtin

import (
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/webhook"
	"io"
	"net/http"
	"net/http/httptest"
)

//nolint:govet // we're using a more descriptive name of example
func ExampleWebhookHTTP() {
	p := webhook.NewWebhookHTTP(log.Nop())
	srv := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		_, _ = resp.Write([]byte("hello, " + string(body)))
	}))

	RunExample(p, example{
		Description: `
This example shows how to use the HTTP processor to send a record's .Payload.After field to a dummy HTTP server 
that replies back with a greeting. 

The record's .Payload.After is overwritten with the response. Additionally, the example shows how to store the 
value of the HTTP response's code in the record's metadata'.`,
		Config: map[string]string{
			"request.url":     srv.URL,
			"request.body":    ".Payload.After",
			"response.status": `.Metadata["http_status"]`,
		},
		Have: opencdc.Record{
			Payload: opencdc.Change{
				After: opencdc.RawData("world"),
			},
		},
		Want: sdk.SingleRecord{
			Metadata: map[string]string{
				"http_status": "200",
			},
			Payload: opencdc.Change{
				After: opencdc.RawData("hello, world"),
			},
		},
	})

	// Output:
	// processor transformed record:
	// --- before
	// +++ after
	// @@ -1,10 +1,12 @@
	//  {
	//    "position": null,
	//    "operation": "Operation(0)",
	// -  "metadata": null,
	// +  "metadata": {
	// +    "http_status": "200"
	// +  },
	//    "key": null,
	//    "payload": {
	//      "before": null,
	// -    "after": "d29ybGQ="
	// +    "after": "aGVsbG8sIHdvcmxk"
	//    }
	//  }
	//
}
