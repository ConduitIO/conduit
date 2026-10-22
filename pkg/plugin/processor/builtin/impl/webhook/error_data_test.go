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
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

// The URL is a template over record fields and can carry credentials, and
// the response body can repeat the request. Errors reach the DLQ, the API and
// CLI output, so none of them may repeat either.
func TestHTTPProcessor_ErrorsDoNotContainTheURLOrTheBody(t *testing.T) {
	rec := opencdc.Record{
		Metadata: opencdc.Metadata{"id": "customer-SECRET-4711", "url": "http://SECRET-user:SECRET-pass@host:" + "badport/x"},
		Payload:  opencdc.Change{After: opencdc.RawData("x")},
	}
	process := func(t *testing.T, url string) error {
		t.Helper()
		ctx := context.Background()
		p := NewHTTPProcessor(log.Nop())
		cfg := config.Config{"request.url": url, "request.method": "POST", "backoffRetry.count": "0"}
		if err := p.Configure(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		out := p.Process(ctx, []opencdc.Record{rec})
		er, ok := out[0].(sdk.ErrorRecord)
		if !ok {
			t.Fatalf("want an error record, got %T", out[0])
		}
		return er.Error
	}

	t.Run("5xx body", func(t *testing.T) {
		is := is.New(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("echo of request: customer-SECRET-4711"))
		}))
		defer srv.Close()
		err := process(t, srv.URL)
		is.True(strings.Contains(err.Error(), "error status code 500"))
		is.True(strings.Contains(err.Error(), "response body: 37 bytes, hash "))
		is.True(!strings.Contains(err.Error(), "SECRET"))
	})

	t.Run("request fails", func(t *testing.T) {
		is := is.New(t)
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close() // nothing listens any more
		err := process(t, url+"/{{ index .Metadata \"id\" }}?token=SECRET-token")
		is.True(strings.Contains(err.Error(), "error executing HTTP request: connection refused")) // the kind, not the host and port
		is.True(!strings.Contains(err.Error(), "127.0.0.1"))
		is.True(!strings.Contains(err.Error(), "SECRET"))
		is.True(!strings.Contains(err.Error(), "token="))
	})

	t.Run("rendered URL does not parse", func(t *testing.T) {
		is := is.New(t)
		err := process(t, "{{ index .Metadata \"url\" }}")
		is.True(strings.Contains(err.Error(), "error parsing the rendered URL"))
		is.True(!strings.Contains(err.Error(), "SECRET"))
	})
}

// The retry loop logs the error of every failed attempt at debug level. With a
// 5xx whose body echoes the request, that log must not carry the body.
func TestHTTPProcessor_RetryLogDoesNotContainTheBody(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("echo of request: customer-SECRET-4711"))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	ctx := context.Background()
	p := NewHTTPProcessor(log.New(zerolog.New(&buf).Level(zerolog.DebugLevel)))
	is.NoErr(p.Configure(ctx, config.Config{
		"request.url": srv.URL, "request.method": "POST",
		"backoffRetry.count": "2", "backoffRetry.min": "1ms", "backoffRetry.max": "2ms",
	}))
	out := p.Process(ctx, []opencdc.Record{{Payload: opencdc.Change{After: opencdc.RawData("x")}}})
	_, ok := out[0].(sdk.ErrorRecord)
	is.True(ok)

	logs := buf.String()
	is.True(strings.Contains(logs, "retrying HTTP request"))
	is.True(strings.Contains(logs, "error status code 500"))
	is.True(!strings.Contains(logs, "SECRET"))
}
