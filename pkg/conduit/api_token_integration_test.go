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

package conduit

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/http/apisec"
	"github.com/gorilla/websocket"
	"github.com/matryer/is"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"

	apiv1 "github.com/conduitio/conduit/proto/api/v1"
)

func withToken(c *Config) { c.API.Token = testToken }

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func TestAPIToken_HTTP(t *testing.T) {
	a := startSecureAPI(t, withToken)
	const wrong = "wrong-token-wrong-token-wrong-token"

	gated := []struct{ method, path string }{
		{"GET", "/v1/pipelines"},
		{"GET", "/v1/plugins"},
		{"GET", "/metrics"},
		{"GET", "/openapi/"},
	}
	for _, g := range gated {
		t.Run(g.method+" "+g.path, func(t *testing.T) {
			is := is.New(t)

			code, body := a.do(t, g.method, g.path, "", nil)
			is.Equal(code, 401) // no token
			is.True(strings.Contains(body, "api.unauthenticated"))

			code, _ = a.do(t, g.method, g.path, "", bearer(wrong))
			is.Equal(code, 401) // wrong token

			code, _ = a.do(t, g.method, g.path, "", map[string]string{"Authorization": "Basic " + testToken})
			is.Equal(code, 401) // right secret, wrong scheme

			code, _ = a.do(t, g.method, g.path, "", bearer(testToken))
			is.Equal(code, 200) // right token
		})
	}

	t.Run("token as the only difference in the last byte is rejected", func(t *testing.T) {
		is := is.New(t)
		code, _ := a.do(t, "GET", "/v1/pipelines", "", bearer(testToken[:len(testToken)-1]+"X"))
		is.Equal(code, 401)
	})

	t.Run("a prefix of the token is rejected", func(t *testing.T) {
		is := is.New(t)
		code, _ := a.do(t, "GET", "/v1/pipelines", "", bearer(testToken[:len(testToken)-1]))
		is.Equal(code, 401)
	})

	t.Run("mutations need the token", func(t *testing.T) {
		is := is.New(t)
		hdr := map[string]string{"Content-Type": "application/json"}
		code, _ := a.do(t, "POST", "/v1/pipelines", `{"config":{"name":"p"}}`, hdr)
		is.Equal(code, 401)
		is.Equal(a.pipelineCount(), 0)

		hdr["Authorization"] = "Bearer " + testToken
		code, _ = a.do(t, "POST", "/v1/pipelines", `{"config":{"name":"p"}}`, hdr)
		is.Equal(code, 200)
		is.Equal(a.pipelineCount(), 1)
	})

	t.Run("probes stay open", func(t *testing.T) {
		is := is.New(t)
		for _, p := range []string{"/healthz", "/readyz"} {
			code, _ := a.do(t, "GET", p, "", nil)
			is.True(code == 200 || code == 503) // answered, not 401
		}
	})

	t.Run("a crafted path cannot borrow the probe exemption", func(t *testing.T) {
		is := is.New(t)
		for _, p := range []string{"/healthz/../v1/pipelines", "/v1/pipelines/../../healthz", "//v1/pipelines", "/v1/pipelines?x=/healthz"} {
			code, _ := a.do(t, "GET", p, "", nil)
			is.True(code == 401 || code == 404 || code == 301 || code == 307) // never 200 without a token
			is.True(code != 200)
		}
	})

	t.Run("inspector websocket needs the token", func(t *testing.T) {
		is := is.New(t)
		d := websocket.Dialer{}
		_, resp, err := d.Dial("ws://"+a.httpAddr+"/v1/connectors/none/inspect", nil)
		is.True(err != nil)
		is.True(resp != nil)
		is.Equal(resp.StatusCode, 401)
		resp.Body.Close()

		conn, resp, err := d.Dial("ws://"+a.httpAddr+"/v1/connectors/none/inspect", map[string][]string{"Authorization": {"Bearer " + testToken}})
		is.NoErr(err)
		is.Equal(resp.StatusCode, 101)
		resp.Body.Close()
		_ = conn.Close()
	})
}

func TestAPIToken_GRPC(t *testing.T) {
	a := startSecureAPI(t, withToken)
	ctx := context.Background()

	dial := func(t *testing.T, opts ...grpc.DialOption) *grpc.ClientConn {
		t.Helper()
		conn, err := grpc.NewClient(a.grpcAddr, append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, opts...)...)
		is.New(t).NoErr(err)
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}

	t.Run("no token", func(t *testing.T) {
		is := is.New(t)
		_, err := apiv1.NewPipelineServiceClient(dial(t)).ListPipelines(ctx, &apiv1.ListPipelinesRequest{})
		is.Equal(status.Code(err), codes.Unauthenticated)
		ce := conduiterr.FromStatus(status.Convert(err))
		is.Equal(ce.Code, apisec.CodeUnauthenticated)
		is.True(ce.Suggestion != "")
	})
	t.Run("wrong token", func(t *testing.T) {
		is := is.New(t)
		conn := dial(t, grpc.WithPerRPCCredentials(apisec.BearerCredentials("wrong-token-wrong-token-wrong")))
		_, err := apiv1.NewPipelineServiceClient(conn).ListPipelines(ctx, &apiv1.ListPipelinesRequest{})
		is.Equal(status.Code(err), codes.Unauthenticated)
	})
	t.Run("right token", func(t *testing.T) {
		is := is.New(t)
		conn := dial(t, grpc.WithPerRPCCredentials(apisec.BearerCredentials(testToken)))
		_, err := apiv1.NewPipelineServiceClient(conn).ListPipelines(ctx, &apiv1.ListPipelinesRequest{})
		is.NoErr(err)
	})
	t.Run("mutation without token creates nothing", func(t *testing.T) {
		is := is.New(t)
		_, err := apiv1.NewPipelineServiceClient(dial(t)).CreatePipeline(ctx, &apiv1.CreatePipelineRequest{Config: &apiv1.Pipeline_Config{Name: "x"}})
		is.Equal(status.Code(err), codes.Unauthenticated)
		is.Equal(a.pipelineCount(), 0)
	})
	t.Run("reflection needs the token", func(t *testing.T) {
		is := is.New(t)
		stream, err := grpc_reflection_v1.NewServerReflectionClient(dial(t)).ServerReflectionInfo(ctx)
		is.NoErr(err)
		is.NoErr(stream.Send(&grpc_reflection_v1.ServerReflectionRequest{
			MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_ListServices{ListServices: ""},
		}))
		_, err = stream.Recv()
		is.Equal(status.Code(err), codes.Unauthenticated)

		conn := dial(t, grpc.WithPerRPCCredentials(apisec.BearerCredentials(testToken)))
		stream, err = grpc_reflection_v1.NewServerReflectionClient(conn).ServerReflectionInfo(ctx)
		is.NoErr(err)
		is.NoErr(stream.Send(&grpc_reflection_v1.ServerReflectionRequest{
			MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_ListServices{ListServices: ""},
		}))
		_, err = stream.Recv()
		is.NoErr(err)
	})
	t.Run("health check stays open", func(t *testing.T) {
		is := is.New(t)
		_, err := grpc_health_v1.NewHealthClient(dial(t)).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
		is.NoErr(err)
	})
}

// A token is enforced on loopback too, and a token file works like an inline
// token.
func TestAPIToken_FromFile(t *testing.T) {
	is := is.New(t)
	f := t.TempDir() + "/token"
	is.NoErr(writeFile(f, "  "+testToken+"\n"))
	a := startSecureAPI(t, func(c *Config) { c.API.TokenFile = f })

	code, _ := a.do(t, "GET", "/v1/pipelines", "", nil)
	is.Equal(code, 401)
	code, _ = a.do(t, "GET", "/v1/pipelines", "", bearer(testToken))
	is.Equal(code, 200)
}

func TestAPIToken_NonLoopbackWithoutTokenRefusesToStart(t *testing.T) {
	for _, addr := range []string{":0", "0.0.0.0:0"} {
		t.Run(addr, func(t *testing.T) {
			is := is.New(t)
			cfg := DefaultConfigWithBasePath(t.TempDir())
			cfg.DB.Type = DBTypeInMemory
			is.NoErr(mkdir(cfg.Pipelines.Path))
			cfg.API.HTTP.Address = addr
			_, err := NewRuntime(cfg)
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, apisec.CodeTokenRequired)
		})
	}
}

func TestAPIToken_NonLoopbackWithTokenServes(t *testing.T) {
	is := is.New(t)
	a := startSecureAPI(t, func(c *Config) {
		c.API.HTTP.Address = "0.0.0.0:0"
		c.API.GRPC.Address = "0.0.0.0:0"
		c.API.Token = testToken
	})
	code, _ := a.do(t, "GET", "/v1/pipelines", "", nil)
	is.Equal(code, 401)
	code, _ = a.do(t, "GET", "/v1/pipelines", "", bearer(testToken))
	is.Equal(code, 200)
}

// A browser cannot set Authorization on a WebSocket, so the UI sends the token
// in Sec-WebSocket-Protocol. These tests drive that carrier against a real
// Runtime.
func TestAPIToken_WebSocketSubprotocolCarrier(t *testing.T) {
	a := startSecureAPI(t, withToken)
	target := "ws://" + a.httpAddr + "/v1/connectors/none/inspect"

	dial := func(protocols ...string) (*websocket.Conn, int) {
		d := websocket.Dialer{Subprotocols: protocols}
		conn, resp, err := d.Dial(target, nil)
		if resp != nil {
			defer resp.Body.Close()
		}
		if err != nil {
			if resp == nil {
				t.Fatalf("dial failed without a response: %v", err)
			}
			return nil, resp.StatusCode
		}
		return conn, resp.StatusCode
	}

	t.Run("right token upgrades and the server selects the plain protocol", func(t *testing.T) {
		is := is.New(t)
		conn, code := dial("conduit", apisec.EncodeWebSocketToken(testToken))
		is.Equal(code, 101)
		is.True(conn != nil)
		is.Equal(conn.Subprotocol(), "conduit") // never the token-bearing one
		_ = conn.Close()
	})
	t.Run("wrong token", func(t *testing.T) {
		is := is.New(t)
		conn, code := dial("conduit", apisec.EncodeWebSocketToken("wrong-token-wrong-token"))
		is.True(conn == nil)
		is.Equal(code, 401)
	})
	t.Run("no token", func(t *testing.T) {
		is := is.New(t)
		conn, code := dial("conduit")
		is.True(conn == nil)
		is.Equal(code, 401)
	})
	t.Run("token in the query string is not accepted", func(t *testing.T) {
		is := is.New(t)
		d := websocket.Dialer{Subprotocols: []string{"conduit"}}
		conn, resp, err := d.Dial(target+"?access_token="+testToken+"&token="+testToken, nil)
		is.True(err != nil)
		is.True(conn == nil)
		is.Equal(resp.StatusCode, 401)
		resp.Body.Close()
	})
	t.Run("a cross-origin page holding the token is still refused", func(t *testing.T) {
		is := is.New(t)
		d := websocket.Dialer{Subprotocols: []string{"conduit", apisec.EncodeWebSocketToken(testToken)}}
		conn, resp, err := d.Dial(target, map[string][]string{"Origin": {"http://evil.example"}})
		is.True(err != nil)
		is.True(conn == nil)
		is.Equal(resp.StatusCode, 403)
		resp.Body.Close()
	})
	t.Run("same-origin page with the token upgrades", func(t *testing.T) {
		is := is.New(t)
		d := websocket.Dialer{Subprotocols: []string{"conduit", apisec.EncodeWebSocketToken(testToken)}}
		conn, resp, err := d.Dial(target, map[string][]string{"Origin": {"http://" + a.httpAddr}})
		is.NoErr(err)
		is.Equal(resp.StatusCode, 101)
		resp.Body.Close()
		_ = conn.Close()
	})
}

// Brute-forcing the token must be visible: every refusal increments a counter
// per code (served on the authenticated /metrics) and the WARN log is rate
// limited rather than one line per guess.
func TestAPIToken_RejectionsAreCountedAndWarnIsRateLimited(t *testing.T) {
	is := is.New(t)
	a := startSecureAPI(t, withToken)

	count := func() float64 {
		_, body := a.do(t, "GET", "/metrics", "", bearer(testToken))
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, `conduit_api_rejected_requests_total{code="api.unauthenticated"}`) {
				var v float64
				_, err := fmt.Sscanf(line[strings.LastIndex(line, " ")+1:], "%g", &v)
				is.NoErr(err)
				return v
			}
		}
		t.Fatalf("counter not exported:\n%s", body)
		return 0
	}
	before := count()
	const attempts = 40
	for i := 0; i < attempts; i++ {
		code, _ := a.do(t, "GET", "/v1/pipelines", "", bearer(fmt.Sprintf("guess-%02d-guess-guess-guess", i)))
		is.Equal(code, 401)
	}
	is.Equal(count()-before, float64(attempts))

	// Series carry only the code label, never request data.
	_, body := a.do(t, "GET", "/metrics", "", bearer(testToken))
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "conduit_api_rejected_requests_total{") {
			is.True(strings.HasPrefix(line, `conduit_api_rejected_requests_total{code="api.`))
			is.True(!strings.Contains(line, "guess"))
		}
	}

	warns := strings.Count(a.logs.String(), "rejected HTTP request: missing or invalid API token")
	is.True(warns >= 1 && warns <= 3) // 40 attempts, a handful of lines at most
	is.True(!strings.Contains(a.logs.String(), "guess-"))
	is.True(!strings.Contains(a.logs.String(), testToken))
}

// The UI must be able to load without a token (it contains no instance data and
// is what prompts for one) while everything it calls stays gated.
func TestAPIToken_UILoadsWithoutTokenAndPromptsViaScript(t *testing.T) {
	is := is.New(t)
	a := startSecureAPI(t, withToken)

	code, body := a.do(t, "GET", "/", "", nil)
	is.Equal(code, 200)
	is.True(strings.Contains(body, `<script src="/conduit-auth.js"></script>`))

	code, body = a.do(t, "GET", "/conduit-auth.js", "", nil)
	is.Equal(code, 200)
	is.True(strings.Contains(body, "sessionStorage"))

	code, _ = a.do(t, "GET", "/v1/pipelines", "", nil)
	is.Equal(code, 401)
}
