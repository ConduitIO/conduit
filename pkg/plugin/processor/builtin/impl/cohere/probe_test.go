// Copyright © 2026 Meroxa, Inc.
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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	cohereClient "github.com/cohere-ai/cohere-go/v2/client"
	"github.com/cohere-ai/cohere-go/v2/option"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// These tests use the real SDK against a server that answers with an error
// whose body repeats the request. They pin two things the fakes cannot: the
// SDK returns pointer error types, so a 5xx is retried, and the error that
// reaches the DLQ names the status and type, never the body.
func TestProcessors_RealSDKErrors(t *testing.T) {
	const secret = "customer-SECRET-4711"
	ctx := context.Background()
	rec := opencdc.Record{Payload: opencdc.Change{After: opencdc.RawData("some text")}}
	retry := config.Config{"backoffRetry.count": "2", "backoffRetry.min": "1ms", "backoffRetry.max": "2ms"}
	with := func(c config.Config) config.Config {
		for k, v := range retry {
			c[k] = v
		}
		return c
	}

	for _, tc := range []struct {
		name    string
		status  int
		retried bool
	}{
		{"unavailable is retried", http.StatusServiceUnavailable, true},
		{"bad request is not", http.StatusBadRequest, false},
	} {
		for proc, run := range map[string]func(url string) sdk.ProcessedRecord{
			"command": func(url string) sdk.ProcessedRecord {
				p := NewCommandProcessor(log.Nop())
				c := with(config.Config{commandProcessorConfigApiKey: "k", commandProcessorConfigPrompt: "p"})
				if err := p.Configure(ctx, c); err != nil {
					t.Fatal(err)
				}
				if err := p.Open(ctx); err != nil {
					t.Fatal(err)
				}
				p.client = &commandClient{client: cohereClient.NewClient(cohereClient.WithBaseURL(url), option.WithMaxAttempts(1)), config: &p.config}
				return p.Process(ctx, []opencdc.Record{rec})[0]
			},
			"rerank": func(url string) sdk.ProcessedRecord {
				p := NewRerankProcessor(log.Nop())
				c := with(config.Config{rerankConfigApiKey: "k", rerankConfigQuery: "q"})
				if err := p.Configure(ctx, c); err != nil {
					t.Fatal(err)
				}
				if err := p.Open(ctx); err != nil {
					t.Fatal(err)
				}
				p.client = &rerankClient{client: cohereClient.NewClient(cohereClient.WithBaseURL(url), option.WithMaxAttempts(1)), config: &p.config}
				return p.Process(ctx, []opencdc.Record{rec})[0]
			},
			"embed": func(url string) sdk.ProcessedRecord {
				p := NewEmbedProcessor(log.Nop())
				c := with(config.Config{embedProcConfigApiKey: "k", embedProcConfigInputType: "classification"})
				if err := p.Configure(ctx, c); err != nil {
					t.Fatal(err)
				}
				if err := p.Open(ctx); err != nil {
					t.Fatal(err)
				}
				p.client = &embedClient{client: cohereClient.NewClient(cohereClient.WithBaseURL(url), option.WithMaxAttempts(1)), config: &p.config}
				return p.Process(ctx, []opencdc.Record{rec})[0]
			},
		} {
			t.Run(proc+"/"+tc.name, func(t *testing.T) {
				is := is.New(t)
				var hits atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					hits.Add(1)
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"message":"echo: ` + secret + `"}`))
				}))
				defer srv.Close()

				er, ok := run(srv.URL).(sdk.ErrorRecord)
				is.True(ok)
				is.True(er.Error != nil)
				is.True(!strings.Contains(er.Error.Error(), secret))
				is.True(strings.Contains(er.Error.Error(), "HTTP status"))
				if tc.retried {
					is.Equal(hits.Load(), int32(3)) // first try and 2 retries by the processor (SDK retries off)
				} else {
					is.Equal(hits.Load(), int32(1))
				}
			})
		}
	}
}
