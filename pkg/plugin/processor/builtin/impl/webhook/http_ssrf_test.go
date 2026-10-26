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

// SSRF regression tests for webhook.http (GHSA-m3mw-fpw8-w765). They build the
// processor the way the registry does when the operator configured nothing:
// no exemptions.

func loopbackServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("internal-secret-response"))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func process(t *testing.T, cfg config.Config, rec opencdc.Record) sdk.ProcessedRecord {
	t.Helper()
	ctx := context.Background()
	p := NewHTTPProcessor(log.Test(t))
	if err := p.Configure(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	out := p.Process(ctx, []opencdc.Record{rec})
	if len(out) != 1 {
		t.Fatalf("got %d results", len(out))
	}
	return out[0]
}

func TestHTTPProcessor_RefusesLoopbackByDefault(t *testing.T) {
	is := is.New(t)
	srv, hits := loopbackServer(t)

	got := process(t, config.Config{"request.url": srv.URL}, opencdc.Record{})

	is.Equal(hits.Load(), int64(0)) // the loopback service must not be reached
	errRec, ok := got.(sdk.ErrorRecord)
	is.True(ok)
	ce, ok := conduiterr.Get(errRec.Error)
	is.True(ok)
	is.Equal(ce.Code.Reason(), "processor.egress_blocked")
	is.True(strings.Contains(ce.Suggestion, "processors.egress.builtin-allow"))
}

// The URL is a template over record data, so a record can pick the target.
func TestHTTPProcessor_RecordDrivenURLCannotReachMetadata(t *testing.T) {
	is := is.New(t)
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://[fd00:ec2::254]/latest/meta-data/",
		"http://[::ffff:169.254.169.254]/",
		"http://2852039166/", // decimal 169.254.169.254
		"http://localhost:6379/",
	} {
		t.Run(target, func(t *testing.T) {
			rec := opencdc.Record{Metadata: opencdc.Metadata{"target": target}}
			got := process(t, config.Config{"request.url": "{{ .Metadata.target }}"}, rec)
			_, ok := got.(sdk.ErrorRecord)
			is.True(ok) // must fail, never produce a response record
		})
	}
}
