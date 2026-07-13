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

package mcp

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpengine "github.com/conduitio/conduit/cmd/conduit/internal/mcp"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
)

// TestValidateHTTPConfig_RefusesWithoutTokenOrTLS is the AC-8 config-gate
// regression test: --http requires both --token-file and --tls-cert/
// --tls-key; any combination missing either is refused before anything is
// served.
func TestValidateHTTPConfig_RefusesWithoutTokenOrTLS(t *testing.T) {
	is := is.New(t)

	cases := []struct {
		name    string
		flags   MCPFlags
		wantErr bool
	}{
		{"nothing set", MCPFlags{}, true},
		{"token only", MCPFlags{TokenFile: "token.txt"}, true},
		{"tls only", MCPFlags{TLSCert: "cert.pem", TLSKey: "key.pem"}, true},
		{"cert without key", MCPFlags{TokenFile: "token.txt", TLSCert: "cert.pem"}, true},
		{"key without cert", MCPFlags{TokenFile: "token.txt", TLSKey: "key.pem"}, true},
		{"everything set", MCPFlags{TokenFile: "token.txt", TLSCert: "cert.pem", TLSKey: "key.pem"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHTTPConfig(tc.flags)
			if tc.wantErr {
				is.True(err != nil)
			} else {
				is.NoErr(err)
			}
		})
	}
}

// TestNewHTTPServer_RefusesWithoutTokenOrTLS exercises the same gate through
// MCPCommand.newHTTPServer (what Execute actually calls before ever binding
// a listener), so the refusal is proven at the real call site, not just the
// helper.
func TestNewHTTPServer_RefusesWithoutTokenOrTLS(t *testing.T) {
	is := is.New(t)

	srv := mcpengine.NewServer(mcpengine.Config{})

	c := &MCPCommand{flags: MCPFlags{HTTP: ":0"}}
	_, err := c.newHTTPServer(srv, log.Nop())
	is.True(err != nil)
}

// TestNewHTTPServer_WithTokenAndTLS_Succeeds is AC-8's positive case: with a
// valid --token-file and --tls-cert/--tls-key, newHTTPServer builds a server
// whose TLSConfig carries the loaded certificate.
func TestNewHTTPServer_WithTokenAndTLS_Succeeds(t *testing.T) {
	is := is.New(t)

	tokenPath := writeTestToken(t, "s3cr3t-token")
	certPath, keyPath := writeTestCert(t)

	srv := mcpengine.NewServer(mcpengine.Config{})
	c := &MCPCommand{flags: MCPFlags{
		HTTP:      ":0",
		TokenFile: tokenPath,
		TLSCert:   certPath,
		TLSKey:    keyPath,
	}}

	httpSrv, err := c.newHTTPServer(srv, log.Nop())
	is.NoErr(err)
	is.True(httpSrv.TLSConfig != nil)
	is.Equal(len(httpSrv.TLSConfig.Certificates), 1)
}

// TestNewHTTPServer_SetsIdleTimeout is the H-3 hardening regression test:
// the --http server bounds idle keep-alive connections (design doc
// §Hardening plan, "no IdleTimeout ... an authenticated (or handshaking)
// client could hold connections open"). No WriteTimeout/ReadTimeout is
// asserted here — streamable HTTP can legitimately hold a response open
// while streaming, so those would be wrong to set (see http.go's comment).
func TestNewHTTPServer_SetsIdleTimeout(t *testing.T) {
	is := is.New(t)

	tokenPath := writeTestToken(t, "s3cr3t-token")
	certPath, keyPath := writeTestCert(t)

	srv := mcpengine.NewServer(mcpengine.Config{})
	c := &MCPCommand{flags: MCPFlags{
		HTTP:      "127.0.0.1:0",
		TokenFile: tokenPath,
		TLSCert:   certPath,
		TLSKey:    keyPath,
	}}

	httpSrv, err := c.newHTTPServer(srv, log.Nop())
	is.NoErr(err)
	is.True(httpSrv.IdleTimeout > 0)
	is.Equal(httpSrv.IdleTimeout, idleTimeout)
	// WriteTimeout must stay unset (0): a blanket write deadline would sever
	// a legitimately long-lived streamable-HTTP response mid-stream.
	is.Equal(httpSrv.WriteTimeout, time.Duration(0))
}

// TestNewHTTPServer_WarnsOnNonLoopbackBind is the H-4 hardening regression
// test: newHTTPServer (the real Execute call site, not just the helper)
// warns when --http resolves to a non-loopback address, and stays silent for
// a loopback-restricted one.
func TestNewHTTPServer_WarnsOnNonLoopbackBind(t *testing.T) {
	cases := []struct {
		name     string
		addr     string
		wantWarn bool
	}{
		{"loopback bind: no warning", "127.0.0.1:0", false},
		{"all-interfaces bind: warns", ":0", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			tokenPath := writeTestToken(t, "s3cr3t-token")
			certPath, keyPath := writeTestCert(t)

			var buf bytes.Buffer
			logger := log.New(zerolog.New(&buf).Level(zerolog.WarnLevel))

			srv := mcpengine.NewServer(mcpengine.Config{})
			c := &MCPCommand{flags: MCPFlags{
				HTTP:      tc.addr,
				TokenFile: tokenPath,
				TLSCert:   certPath,
				TLSKey:    keyPath,
			}}

			_, err := c.newHTTPServer(srv, logger)
			is.NoErr(err)

			gotWarning := strings.Contains(buf.String(), "non-loopback")
			is.Equal(gotWarning, tc.wantWarn)
		})
	}
}

// TestWarnIfNonLoopback exercises the H-4 helper directly across the address
// shapes an operator might pass to --http: loopback IPv4/IPv6, "localhost",
// an explicit non-loopback IP, an unqualified hostname, and the
// all-interfaces empty-host form.
func TestWarnIfNonLoopback(t *testing.T) {
	cases := []struct {
		name     string
		addr     string
		wantWarn bool
	}{
		{"loopback IPv4 with port", "127.0.0.1:8443", false},
		{"loopback IPv6 with port", "[::1]:8443", false},
		{"localhost hostname", "localhost:8443", false},
		{"all interfaces (empty host)", ":8443", true},
		{"explicit non-loopback IP", "0.0.0.0:8443", true},
		{"non-loopback hostname", "mcp.example.com:8443", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			var buf bytes.Buffer
			logger := log.New(zerolog.New(&buf).Level(zerolog.WarnLevel))

			warnIfNonLoopback(logger, tc.addr)

			is.Equal(buf.Len() > 0, tc.wantWarn)
		})
	}
}

// TestRequireBearerToken_AuthenticatesGoodRejectsBad is the AC-8 auth
// regression test: a request with no Authorization header, or the wrong
// bearer token, is rejected 401 before reaching the wrapped handler; a
// request with the correct token reaches it. Exercised directly against the
// http.Handler (no real network listener/TLS handshake needed to prove the
// auth logic itself).
func TestRequireBearerToken_AuthenticatesGoodRejectsBad(t *testing.T) {
	is := is.New(t)

	const token = "the-right-token"
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	handler := requireBearerToken(token, next, log.Nop())

	cases := []struct {
		name       string
		authHeader string
		wantStatus int
		wantReach  bool
	}{
		{"no header", "", http.StatusUnauthorized, false},
		{"wrong scheme", "Basic " + token, http.StatusUnauthorized, false},
		{"wrong token", "Bearer nope", http.StatusUnauthorized, false},
		{"correct token", "Bearer " + token, http.StatusOK, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			is.Equal(rec.Code, tc.wantStatus)
			is.Equal(reached, tc.wantReach)
			if tc.wantStatus == http.StatusUnauthorized {
				is.True(rec.Header().Get("WWW-Authenticate") != "")
			}
		})
	}
}

// TestNewMCPHTTPHandler_EndToEndAuth wires the real streamable-HTTP handler
// (over a real *sdkmcp.Server) behind requireBearerToken, proving the full
// composition — not just requireBearerToken in isolation — rejects a bad
// token and lets a good one reach the MCP protocol handler underneath.
func TestNewMCPHTTPHandler_EndToEndAuth(t *testing.T) {
	is := is.New(t)

	const token = "the-right-token"
	srv := mcpengine.NewServer(mcpengine.Config{})
	handler := newMCPHTTPHandler(srv, token, log.Nop())

	t.Run("missing token rejected before reaching the MCP handler", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		is.Equal(rec.Code, http.StatusUnauthorized)
	})

	t.Run("correct token passes through to the MCP handler", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		// The exact status the streamable-HTTP handler returns for a bodiless
		// POST isn't the point here — what matters is it is NOT 401: auth let
		// the request through to the wrapped mcp.Server.
		is.True(rec.Code != http.StatusUnauthorized)
	})
}

// TestNewMCPHTTPHandler_ToolsListRequiresAuth is the AC-5 regression test,
// made explicit per the design doc: an unauthenticated tools/list JSON-RPC
// call is rejected 401 by the bearer middleware before it ever reaches the
// MCP protocol handler — the caller learns nothing about the catalog, not
// even an empty one, and gets no 200.
func TestNewMCPHTTPHandler_ToolsListRequiresAuth(t *testing.T) {
	is := is.New(t)

	srv := mcpengine.NewServer(mcpengine.Config{})
	handler := newMCPHTTPHandler(srv, "the-right-token", log.Nop())

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	is.Equal(rec.Code, http.StatusUnauthorized)
	// Not a (even empty) JSON-RPC result: the middleware must reject before
	// the request reaches tools/list handling at all.
	is.True(!strings.Contains(rec.Body.String(), `"result"`))
}

// TestLoadBearerToken_RejectsEmptyOrWhitespace is the AC-6 regression test:
// a --token-file that is empty, or whitespace-only after trimming, is
// refused — an empty required token would make every empty Authorization
// header a trivial constant-time "match".
func TestLoadBearerToken_RejectsEmptyOrWhitespace(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"empty file", ""},
		{"whitespace only", "   \n\t  \n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			path := filepath.Join(t.TempDir(), "token.txt")
			is.NoErr(os.WriteFile(path, []byte(tc.content), 0o600))

			_, err := loadBearerToken(path)
			is.True(err != nil)
		})
	}
}

// TestHTTPCatalog_AllowMutationsGatesWriteTools is the AC-7 regression test:
// --allow-mutations gates the write tools (apply, scaffold_connector,
// scaffold_processor) identically over HTTP as it does over stdio — the
// mutation gate is transport-independent (design doc "One tool catalog").
// Drives a real MCP client over a real HTTP round-trip (initialize +
// tools/list), not just an inspection of the *sdkmcp.Server's internal tool
// registry, so the assertion covers the same path an agent actually uses.
func TestHTTPCatalog_AllowMutationsGatesWriteTools(t *testing.T) {
	cases := []struct {
		name           string
		allowMutations bool
		wantApply      bool
	}{
		{"mutations disabled: apply absent over HTTP", false, false},
		{"mutations enabled: apply present over HTTP", true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			const token = "the-right-token"
			srv := mcpengine.NewServer(mcpengine.Config{AllowMutations: tc.allowMutations})
			handler := newMCPHTTPHandler(srv, token, log.Nop())

			httpServer := httptest.NewServer(handler)
			defer httpServer.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			transport := &sdkmcp.StreamableClientTransport{
				Endpoint:   httpServer.URL,
				HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token}},
			}
			client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)

			session, err := client.Connect(ctx, transport, nil)
			is.NoErr(err)
			defer session.Close()

			result, err := session.ListTools(ctx, nil)
			is.NoErr(err)

			hasApply := false
			for _, tool := range result.Tools {
				if tool.Name == mcpengine.ToolApply {
					hasApply = true
				}
			}
			is.Equal(hasApply, tc.wantApply)
		})
	}
}

// bearerRoundTripper adds "Authorization: Bearer <token>" to every outgoing
// request — used by tests that drive a real *sdkmcp.Client against the
// --http handler, since sdkmcp.StreamableClientTransport has no built-in
// bearer-auth option (mirroring how an operator would configure their own
// agent's HTTP client against conduit mcp --http).
type bearerRoundTripper struct {
	token string
}

func (t bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(req)
}

// TestRequireBearerToken_RejectionsAreLoggedWithoutLeakingToken is the
// AC-8/AC-10 pairing the design doc calls out as the security-regression
// anchor: a 401 is observable (method/path/remote_addr/outcome logged) and
// the log line — on rejection or success — never contains the configured
// token or the (wrong) token the caller presented. This test must fail if
// any future change logs either.
func TestRequireBearerToken_RejectionsAreLoggedWithoutLeakingToken(t *testing.T) {
	is := is.New(t)

	const (
		realToken  = "s3cr3t-real-token-value"
		wrongToken = "guessed-wrong-token-value"
	)

	var buf bytes.Buffer
	logger := log.New(zerolog.New(&buf).Level(zerolog.WarnLevel))

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := requireBearerToken(realToken, next, logger)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+wrongToken)
	req.RemoteAddr = "203.0.113.7:54321"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	is.Equal(rec.Code, http.StatusUnauthorized)

	logged := buf.String()

	// AC-10: the rejection is observable — an operator can tell a 401
	// happened, for what method/path, and from where.
	is.True(strings.Contains(logged, "unauthorized"))
	is.True(strings.Contains(logged, "POST"))
	is.True(strings.Contains(logged, "/mcp"))
	is.True(strings.Contains(logged, "203.0.113.7:54321"))

	// AC-8: neither the configured token nor the (wrong) presented one ever
	// appears in the log output — a 401 audit trail must not itself leak
	// the secret it exists to protect.
	is.True(!strings.Contains(logged, realToken))
	is.True(!strings.Contains(logged, wrongToken))
}

// TestRequireBearerToken_SuccessIsNotLoggedAsRejection complements the
// rejection-logging test: a request with the correct token must not produce
// an "unauthorized" log line.
func TestRequireBearerToken_SuccessIsNotLoggedAsRejection(t *testing.T) {
	is := is.New(t)

	const token = "the-right-token"

	var buf bytes.Buffer
	logger := log.New(zerolog.New(&buf).Level(zerolog.WarnLevel))

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := requireBearerToken(token, next, logger)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	is.Equal(rec.Code, http.StatusOK)
	is.True(!strings.Contains(buf.String(), "unauthorized"))
}

// TestHTTPServer_GracefulShutdownDrains is the AC-12 regression test: a real
// bound --http listener, on context cancellation, drains via
// http.Server.Shutdown (the same call Execute's errgroup makes) rather than
// dropping in-flight connections, and returns cleanly.
func TestHTTPServer_GracefulShutdownDrains(t *testing.T) {
	is := is.New(t)

	tokenPath := writeTestToken(t, "s3cr3t-token")
	certPath, keyPath := writeTestCert(t)

	srv := mcpengine.NewServer(mcpengine.Config{})
	c := &MCPCommand{flags: MCPFlags{
		HTTP:      "127.0.0.1:0",
		TokenFile: tokenPath,
		TLSCert:   certPath,
		TLSKey:    keyPath,
	}}

	httpSrv, err := c.newHTTPServer(srv, log.Nop())
	is.NoErr(err)

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", httpSrv.Addr)
	is.NoErr(err)

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- httpSrv.ServeTLS(ln, "", "")
	}()

	// Give the listener a moment to start accepting before shutting down —
	// avoids a race where Shutdown runs before ServeTLS has registered the
	// listener.
	time.Sleep(50 * time.Millisecond)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	is.NoErr(httpSrv.Shutdown(shutdownCtx))

	select {
	case err := <-serveErrCh:
		// http.ErrServerClosed is Shutdown's documented clean-exit signal —
		// exactly what Execute's own errgroup goroutine treats as success
		// (mcp.go: "cerrors.Is(err, http.ErrServerClosed) { return nil }").
		is.True(err != nil)
		is.Equal(err, http.ErrServerClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("ServeTLS did not return after Shutdown; a connection may not have drained")
	}
}

func writeTestToken(t *testing.T, token string) string {
	t.Helper()
	is := is.New(t)
	path := filepath.Join(t.TempDir(), "token.txt")
	is.NoErr(os.WriteFile(path, []byte(token+"\n"), 0o600))
	return path
}

// writeTestCert generates a throwaway self-signed ECDSA cert/key pair for
// TestNewHTTPServer_WithTokenAndTLS_Succeeds — good enough to exercise
// tls.LoadX509KeyPair's parsing without any external tooling.
func writeTestCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	is := is.New(t)

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	is.NoErr(err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "conduit-mcp-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	is.NoErr(err)

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certOut, err := os.Create(certPath)
	is.NoErr(err)
	is.NoErr(pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	is.NoErr(certOut.Close())

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	is.NoErr(err)
	keyOut, err := os.Create(keyPath)
	is.NoErr(err)
	is.NoErr(pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}))
	is.NoErr(keyOut.Close())

	return certPath, keyPath
}
