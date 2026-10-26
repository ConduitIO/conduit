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

package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

// SSRF regression for ollama.request (GHSA-m3mw-fpw8-w765): it used to use
// http.DefaultClient, so the `url` setting could point it at any internal
// service. A local Ollama now needs an operator exemption.
func TestOllama_RefusesLoopbackByDefault(t *testing.T) {
	is := is.New(t)

	prev := HTTPClient
	HTTPClient = nil // use the processor's own (guarded) client, not a test double
	t.Cleanup(func() { HTTPClient = prev })

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"response":"{}","done":true}`))
	}))
	defer srv.Close()

	ctx := context.Background()
	p := NewOllamaProcessor(log.Nop())
	is.NoErr(p.Configure(ctx, config.Config{"url": srv.URL, "model": "llama3.2", "prompt": "x", "field": ".Payload.After"}))

	out := p.Process(ctx, []opencdc.Record{{
		Operation: opencdc.OperationCreate,
		Payload:   opencdc.Change{After: opencdc.StructuredData{"a": 1}},
	}})

	is.Equal(hits.Load(), int64(0)) // the loopback service must not be reached
	errRec, ok := out[len(out)-1].(sdk.ErrorRecord)
	is.True(ok)
	ce, ok := conduiterr.Get(errRec.Error)
	is.True(ok)
	is.Equal(ce.Code.Reason(), "processor.egress_blocked")
	is.True(strings.Contains(ce.Suggestion, "processors.egress.builtin-allow"))
}
