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

package apisec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRejections_CounterPerCodeAndBoundedLabels(t *testing.T) {
	is := is.New(t)
	reg := promclient.NewRegistry()
	rej := NewRejections(reg, log.Nop())

	for i := 0; i < 3; i++ {
		rej.Record(context.Background(), CodeUnauthenticated, "1.2.3.4:5", "x")
	}
	rej.Record(context.Background(), CodeOriginForbidden, "1.2.3.4:5", "x")

	cv := rej.counter
	is.Equal(testutil.ToFloat64(cv.WithLabelValues("api.unauthenticated")), 3.0)
	is.Equal(testutil.ToFloat64(cv.WithLabelValues("api.origin_forbidden")), 1.0)
	is.Equal(testutil.ToFloat64(cv.WithLabelValues("api.host_forbidden")), 0.0)

	// Only the fixed code label exists, and every series is one of the
	// registered rejection reasons: nothing from a request becomes a label.
	mfs, err := reg.Gather()
	is.NoErr(err)
	is.Equal(len(mfs), 1)
	is.Equal(mfs[0].GetName(), RejectionsMetricName)
	allowed := map[string]bool{}
	for _, c := range rejectionCodes {
		allowed[c.Reason()] = true
	}
	for _, m := range mfs[0].GetMetric() {
		is.Equal(len(m.GetLabel()), 1)
		is.Equal(m.GetLabel()[0].GetName(), "code")
		is.True(allowed[m.GetLabel()[0].GetValue()])
	}
}

func TestRejections_SecondRegistrationSharesTheCounter(t *testing.T) {
	is := is.New(t)
	reg := promclient.NewRegistry()
	a := NewRejections(reg, log.Nop())
	b := NewRejections(reg, log.Nop())
	a.Record(context.Background(), CodeUnauthenticated, "", "x")
	b.Record(context.Background(), CodeUnauthenticated, "", "x")
	is.Equal(testutil.ToFloat64(a.counter.WithLabelValues("api.unauthenticated")), 2.0)
}

func TestRejections_NilIsSafe(t *testing.T) {
	var r *Rejections
	r.Record(context.Background(), CodeUnauthenticated, "", "x") // must not panic
}

func TestRejections_WarnIsRateLimitedPerCode(t *testing.T) {
	is := is.New(t)
	var sb strings.Builder
	rej := NewRejections(nil, log.New(zerologNew(&sb)))
	now := time.Unix(1000, 0)
	rej.now = func() time.Time { return now }
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		rej.Record(ctx, CodeUnauthenticated, "1.2.3.4:5", "rejected")
	}
	is.Equal(strings.Count(sb.String(), "rejected"), 1) // one line for 50 attempts

	// A different code has its own budget.
	rej.Record(ctx, CodeOriginForbidden, "1.2.3.4:5", "rejected")
	is.Equal(strings.Count(sb.String(), "rejected"), 2)

	// After the interval the next line reports how many were held back.
	now = now.Add(rejectionWarnInterval + time.Second)
	rej.Record(ctx, CodeUnauthenticated, "1.2.3.4:5", "rejected")
	is.Equal(strings.Count(sb.String(), "rejected"), 3)
	is.True(strings.Contains(sb.String(), `"suppressed_since_last":49`))
}

func TestAuthenticator_CountsRejections(t *testing.T) {
	is := is.New(t)
	reg := promclient.NewRegistry()
	rej := NewRejections(reg, log.Nop())
	next, _ := okHandler()
	h := NewAuthenticator(goodToken, rej).HTTP(next)
	for i := 0; i < 5; i++ {
		req := httptest.NewRequestWithContext(context.Background(), "GET", "/v1/pipelines", nil)
		req.Header.Set("Authorization", "Bearer guess-guess-guess-guess")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	is.Equal(testutil.ToFloat64(rej.counter.WithLabelValues("api.unauthenticated")), 5.0)
}

func wsUpgradeRequest(protocols ...string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/v1/connectors/x/inspect", nil)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Connection", "Upgrade")
	for _, p := range protocols {
		r.Header.Add("Sec-WebSocket-Protocol", p)
	}
	return r
}

func TestAuthenticator_WebSocketSubprotocolCarrier(t *testing.T) {
	for name, tc := range map[string]struct {
		req  func() *http.Request
		want int
	}{
		"right token": {func() *http.Request {
			return wsUpgradeRequest("conduit", EncodeWebSocketToken(goodToken))
		}, 200},
		"right token, one header line": {func() *http.Request {
			return wsUpgradeRequest("conduit, " + EncodeWebSocketToken(goodToken))
		}, 200},
		"wrong token": {func() *http.Request {
			return wsUpgradeRequest("conduit", EncodeWebSocketToken("wrong-wrong-wrong-wrong"))
		}, 401},
		"prefix of the token": {func() *http.Request {
			return wsUpgradeRequest("conduit", EncodeWebSocketToken(goodToken[:len(goodToken)-1]))
		}, 401},
		"no carrier": {func() *http.Request { return wsUpgradeRequest("conduit") }, 401},
		"malformed base64": {func() *http.Request {
			return wsUpgradeRequest("conduit", WebSocketTokenPrefix+"!!!")
		}, 401},
		"empty token": {func() *http.Request { return wsUpgradeRequest("conduit", WebSocketTokenPrefix) }, 401},
		"query string token is never read": {func() *http.Request {
			r := wsUpgradeRequest("conduit")
			r.URL.RawQuery = "access_token=" + goodToken + "&token=" + goodToken
			return r
		}, 401},
		"carrier on a plain (non-upgrade) request is ignored": {func() *http.Request {
			r := wsUpgradeRequest("conduit", EncodeWebSocketToken(goodToken))
			r.Header.Del("Upgrade")
			return r
		}, 401},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			var seen http.Header
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.Header.Clone() })
			rec := httptest.NewRecorder()
			NewAuthenticator(goodToken, NewRejections(nil, log.Nop())).HTTP(next).ServeHTTP(rec, tc.req())
			is.Equal(rec.Code, tc.want)
			if tc.want == 200 {
				// The carrier never travels past the gate; the plain protocol does.
				is.True(!strings.Contains(strings.Join(seen.Values("Sec-WebSocket-Protocol"), ","), "conduit.token."))
				is.Equal(seen.Get("Sec-WebSocket-Protocol"), "conduit")
			}
		})
	}
}

func TestEncodeWebSocketToken_RoundTrip(t *testing.T) {
	is := is.New(t)
	for _, tok := range []string{goodToken, "with spaces and / + = symbols é"} {
		r := wsUpgradeRequest("conduit", EncodeWebSocketToken(tok))
		got, ok := takeWebSocketToken(r)
		is.True(ok)
		is.Equal(got, tok)
	}
	// Carrier characters are valid RFC 6455 subprotocol token characters.
	enc := EncodeWebSocketToken("with spaces and / + = symbols é")
	is.True(!strings.ContainsAny(enc, " =/+,;\""))
}
