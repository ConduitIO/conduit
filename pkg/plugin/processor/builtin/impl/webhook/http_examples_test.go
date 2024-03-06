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

package webhook

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"

	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	conduit_log "github.com/conduitio/conduit/pkg/foundation/log"
)

//nolint:govet // we're using a more descriptive name of example
func ExampleHTTPProcessor() {
	p := NewHTTPProcessor(conduit_log.Nop())

	srv := newTestServer()
	// Stop the server on return from the function.
	defer srv.Close()

	exampleutil.RunExample(p, exampleutil.Example{
		Summary: `Send a request to an HTTP server`,
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
	// -    "after": "world"
	// +    "after": "hello, world"
	//    }
	//  }
}

func newTestServer() *httptest.Server {
	l, err := net.Listen("tcp", "127.0.0.1:54321")
	if err != nil {
		log.Fatalf("failed starting test server on port 54321: %v", err)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		_, _ = resp.Write([]byte("hello, " + string(body)))
	}))

	// NewUnstartedServer creates a listener. Close that listener and replace
	// with the one we created.
	srv.Listener.Close()
	srv.Listener = l

	// Start the server.
	srv.Start()

	return srv
}
