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
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress/egresstest"
	"github.com/matryer/is"
)

// An exempted public-looking endpoint can still not bounce the request onto a
// blocked one.
func TestHTTPProcessor_RedirectToBlockedTargetIsRefused(t *testing.T) {
	internal, internalHits := loopbackServer(t)
	internalURL := internal.URL

	for name, location := range map[string]string{
		"metadata":         "http://169.254.169.254/latest/meta-data/",
		"another loopback": internalURL,
		"mapped loopback":  "http://[::ffff:127.0.0.1]:1/",
		"private range":    "http://10.0.0.1/",
		"decimal loopback": "http://2130706433/",
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, location, http.StatusFound)
			}))
			defer redirector.Close()

			ctx := context.Background()
			p := NewHTTPProcessor(log.Test(t))
			p.SetDialGuard(egresstest.Guard(t, redirector))
			is.NoErr(p.Configure(ctx, config.Config{"request.url": redirector.URL}))

			out := p.Process(ctx, []opencdc.Record{{}})
			errRec, ok := out[0].(sdk.ErrorRecord)
			is.True(ok)
			ce, ok := conduiterr.Get(errRec.Error)
			is.True(ok)
			is.Equal(ce.Code.Reason(), "processor.egress_blocked")
		})
	}
	is.New(t).Equal(internalHits.Load(), int64(0))
}

func TestHTTPProcessor_OperatorExemptionWorks(t *testing.T) {
	is := is.New(t)
	srv, hits := loopbackServer(t)

	ctx := context.Background()
	p := NewHTTPProcessor(log.Test(t))
	p.SetDialGuard(egresstest.Guard(t, srv))
	is.NoErr(p.Configure(ctx, config.Config{"request.url": srv.URL}))
	out := p.Process(ctx, []opencdc.Record{{}})
	_, ok := out[0].(sdk.SingleRecord)
	is.True(ok)
	is.Equal(hits.Load(), int64(1))
}
