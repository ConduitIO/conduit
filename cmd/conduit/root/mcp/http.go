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
	"crypto/subtle"
	"crypto/tls"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// bearerPrefix is the standard HTTP Authorization scheme this server
// requires.
const bearerPrefix = "Bearer "

// newHTTPServer builds the *http.Server for --http: it refuses outright
// (design doc §5, AC-8) unless both a bearer token (--token-file) and a TLS
// certificate/key (--tls-cert/--tls-key) are configured — stdio is the only
// transport that needs no auth (the agent owns that process); HTTP may be
// reachable by anyone who can open a socket to it, so it never serves
// without both checks passing.
func (c *MCPCommand) newHTTPServer(srv *sdkmcp.Server) (*http.Server, error) {
	if err := validateHTTPConfig(c.flags); err != nil {
		return nil, err
	}

	token, err := loadBearerToken(c.flags.TokenFile)
	if err != nil {
		return nil, err
	}

	cert, err := tls.LoadX509KeyPair(c.flags.TLSCert, c.flags.TLSKey)
	if err != nil {
		return nil, conduiterr.Wrap(conduiterr.CodeInvalidArgument, "could not load --tls-cert/--tls-key", err)
	}

	// Bound the request body: a tool call carries a pipeline config (KBs), so a
	// few MiB is generous. Without this an authenticated client could post an
	// arbitrarily large body that is buffered/parsed in memory (a DoS vector,
	// blast-radius-limited to token holders, but cheap to close).
	handler := limitRequestBody(newMCPHTTPHandler(srv, token), maxRequestBodyBytes)

	return &http.Server{
		Addr:      c.flags.HTTP,
		Handler:   handler,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		// ReadHeaderTimeout bounds a slow/malicious client that trickles
		// request headers (a Slowloris-style attack) — required on any
		// http.Server exposed beyond localhost.
		ReadHeaderTimeout: 10 * time.Second,
	}, nil
}

// maxRequestBodyBytes bounds an HTTP MCP request body. 4 MiB is generous for a
// tool call carrying a pipeline config plus JSON-RPC framing, while capping the
// memory an authenticated caller can force the server to buffer.
const maxRequestBodyBytes = 4 << 20 // 4 MiB

// limitRequestBody caps every request body at max bytes; a request that exceeds
// it fails the read with http.MaxBytesError (413-shaped) rather than being
// buffered in full.
func limitRequestBody(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}

// validateHTTPConfig reports whether flags carry everything --http requires
// (a non-empty --token-file and both --tls-cert/--tls-key). It does not
// touch the filesystem — see loadBearerToken/tls.LoadX509KeyPair for the
// checks that a configured path is actually readable/valid.
func validateHTTPConfig(flags MCPFlags) error {
	switch {
	case flags.TokenFile == "" && (flags.TLSCert == "" || flags.TLSKey == ""):
		ce := conduiterr.New(conduiterr.CodeInvalidArgument,
			"--http requires both --token-file and --tls-cert/--tls-key; refusing to serve HTTP with no auth and no TLS")
		ce.Suggestion = "pass --token-file <path> and --tls-cert/--tls-key <paths>, or drop --http to use stdio only"
		return ce
	case flags.TokenFile == "":
		ce := conduiterr.New(conduiterr.CodeInvalidArgument, "--http requires --token-file; refusing to serve HTTP with no bearer token configured")
		ce.Suggestion = "pass --token-file <path to a file containing the bearer token>"
		return ce
	case flags.TLSCert == "" || flags.TLSKey == "":
		ce := conduiterr.New(conduiterr.CodeInvalidArgument, "--http requires --tls-cert and --tls-key; refusing to serve HTTP in plaintext")
		ce.Suggestion = "pass --tls-cert <path> and --tls-key <path>"
		return ce
	}
	return nil
}

// loadBearerToken reads and trims the bearer token --token-file points at.
// An empty (or empty-after-trim) file is rejected — an empty required token
// would make every constant-time comparison against an empty Authorization
// header trivially "match", which is not a token requirement at all.
func loadBearerToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		ce := conduiterr.Wrap(conduiterr.CodeInvalidArgument, "could not read --token-file", err)
		ce.Suggestion = "check that the file exists and is readable"
		return "", ce
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		ce := conduiterr.New(conduiterr.CodeInvalidArgument, "--token-file is empty")
		ce.Suggestion = "put a non-empty bearer token in the file --token-file points at"
		return "", ce
	}
	return token, nil
}

// newMCPHTTPHandler mounts the go-sdk's streamable-HTTP handler (the same
// *sdkmcp.Server, and therefore the same tool catalog, as stdio — design doc
// §2: "stdio + --http serve one tool catalog") behind the bearer-token
// middleware. getServer always returns srv (a single, already-built server
// instance is fine to hand back for every session — see
// mcp.NewStreamableHTTPHandler's doc).
func newMCPHTTPHandler(srv *sdkmcp.Server, token string) http.Handler {
	mcpHandler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return srv }, nil)
	return requireBearerToken(token, mcpHandler)
}

// requireBearerToken wraps next with constant-time bearer-token
// authentication (design doc §5: "a bearer token, compared constant-time").
// A missing/malformed Authorization header or a token that doesn't match is
// rejected with 401 before next ever sees the request.
func requireBearerToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), bearerPrefix)
		if !ok || !constantTimeEqual(presented, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="conduit-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// constantTimeEqual reports whether a and b are equal, comparing in time
// independent of *where* they first differ once lengths match (a length
// mismatch itself is not the secret, so short-circuiting on that alone
// leaks nothing a timing attack could exploit about the token's contents).
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
