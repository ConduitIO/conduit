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

package webhook

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// TestHTTPProcessor_TraceLogDoesNotContainRecordKey: the trace line for a
// record carries the key's size and a salted hash, never the key.
func TestHTTPProcessor_TraceLogDoesNotContainRecordKey(t *testing.T) {
	is := is.New(t)
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.TraceLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	ctx := context.Background()
	p := NewHTTPProcessor(log.New(zerolog.New(&buf).Level(zerolog.TraceLevel)))
	is.NoErr(p.Configure(ctx, config.Config{"request.url": srv.URL, "request.method": "POST"}))

	const secret = "customer-SECRET-4711"
	p.Process(ctx, []opencdc.Record{{
		Key:     opencdc.RawData(secret),
		Payload: opencdc.Change{After: opencdc.RawData("x")},
	}})

	out := buf.String()
	is.True(strings.Contains(out, "processing record"))
	is.True(strings.Contains(out, `"record_key_hash"`))
	is.True(strings.Contains(out, `"record_key_size":20`))
	is.True(!strings.Contains(out, secret))
}
