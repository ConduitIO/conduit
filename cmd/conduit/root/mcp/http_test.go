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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	mcpengine "github.com/conduitio/conduit/cmd/conduit/internal/mcp"
	"github.com/matryer/is"
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
	_, err := c.newHTTPServer(srv)
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

	httpSrv, err := c.newHTTPServer(srv)
	is.NoErr(err)
	is.True(httpSrv.TLSConfig != nil)
	is.Equal(len(httpSrv.TLSConfig.Certificates), 1)
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
	handler := requireBearerToken(token, next)

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
	handler := newMCPHTTPHandler(srv, token)

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
