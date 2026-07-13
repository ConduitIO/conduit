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
	"crypto/subtle"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
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
//
// logger is used for the H-2/H-4 hardening additions (design doc
// 20260712-mcp-http-transport.md, §Hardening plan): a startup line naming
// the bound address and auth mode, a warning if the bind address is not
// loopback-restricted, and (via newMCPHTTPHandler) a log line per rejected
// auth attempt. Never pass anything derived from the token itself to logger
// — see requireBearerToken's invariant comment (AC-8).
func (c *MCPCommand) newHTTPServer(srv *sdkmcp.Server, logger log.CtxLogger) (*http.Server, error) {
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

	// H-4: TLS + a bearer token make a non-loopback bind *safe*, but a
	// surprised operator exposing the MCP endpoint (a mutation-capable
	// surface when --allow-mutations is set) to the network unintentionally
	// is a real failure mode — surface it at startup instead of staying
	// silent.
	warnIfNonLoopback(logger, c.flags.HTTP)

	// Bound the request body: a tool call carries a pipeline config (KBs), so a
	// few MiB is generous. Without this an authenticated client could post an
	// arbitrarily large body that is buffered/parsed in memory (a DoS vector,
	// blast-radius-limited to token holders, but cheap to close).
	handler := limitRequestBody(newMCPHTTPHandler(srv, token, logger), maxRequestBodyBytes)

	logger.Info(context.Background()).
		Str("addr", c.flags.HTTP).
		Str("auth", "bearer").
		Bool("tls", true).
		Msg("serving MCP over streamable HTTP")

	return &http.Server{
		Addr:      c.flags.HTTP,
		Handler:   handler,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		// ReadHeaderTimeout bounds a slow/malicious client that trickles
		// request headers (a Slowloris-style attack) — required on any
		// http.Server exposed beyond localhost.
		ReadHeaderTimeout: 10 * time.Second,
		// IdleTimeout (H-3) bounds how long a keep-alive connection may sit
		// idle between requests. Streamable HTTP can hold a response open
		// while streaming, so a blanket WriteTimeout would be wrong (it
		// would sever a legitimately long-lived response); IdleTimeout only
		// closes connections that are not in the middle of a request/
		// response, which is safe and still closes out connection-
		// exhaustion attempts that never send a next request.
		IdleTimeout: idleTimeout,
	}, nil
}

// idleTimeout bounds an idle (no in-flight request) keep-alive connection on
// the --http listener (design doc H-3). 120s is generous for a legitimate
// agent reusing a connection across tool calls while still reclaiming
// connections an attacker opens and leaves idle.
const idleTimeout = 120 * time.Second

// warnIfNonLoopback logs a warning (design doc H-4) if addr — the --http
// bind address — is not restricted to loopback. An empty host (e.g. ":8443")
// binds all interfaces and counts as non-loopback. TLS and the bearer token
// already make this safe; the warning exists so an operator who intended
// `--http localhost:8443` and typo'd or defaulted to `--http :8443` notices
// the wider exposure instead of discovering it later.
func warnIfNonLoopback(logger log.CtxLogger, addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// addr had no port (unusual for --http, but don't crash logging over
		// it); fall back to treating the whole value as the host part.
		host = addr
	}

	if host != "" {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return
		}
		if host == "localhost" {
			return
		}
	}

	logger.Warn(context.Background()).
		Str("addr", addr).
		Msg("--http is bound to a non-loopback address; the MCP server will be reachable from the network (TLS + bearer token are still required, but confirm this exposure is intentional)")
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
func newMCPHTTPHandler(srv *sdkmcp.Server, token string, logger log.CtxLogger) http.Handler {
	mcpHandler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return srv }, nil)
	return requireBearerToken(token, mcpHandler, logger)
}

// requireBearerToken wraps next with constant-time bearer-token
// authentication (design doc §5: "a bearer token, compared constant-time").
// A missing/malformed Authorization header or a token that doesn't match is
// rejected with 401 before next ever sees the request; the rejection is
// logged (design doc H-2/AC-10) so brute-force/probing attempts against the
// token are observable instead of invisible.
func requireBearerToken(token string, next http.Handler, logger log.CtxLogger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), bearerPrefix)
		if !ok || !constantTimeEqual(presented, token) {
			// Invariant (AC-8): never log the presented credential or the
			// configured token — only request metadata an operator needs to
			// spot brute-forcing (method/path/remote addr), never the
			// secret the log line exists to protect.
			logger.Warn(r.Context()).
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Str("remote_addr", r.RemoteAddr).
				Str("outcome", "unauthorized").
				Msg("rejected MCP HTTP request: missing or invalid bearer token")
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
