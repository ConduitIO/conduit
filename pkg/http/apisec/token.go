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
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

const (
	// MinTokenLength is the shortest accepted API token. Tokens are compared
	// byte for byte, so length is the only brute-force defence.
	MinTokenLength = 16

	// ConfigPathToken and ConfigPathTokenFile are the config paths reported
	// in token-related refusals.
	ConfigPathToken     = "api.token"
	ConfigPathTokenFile = "api.token-file"

	bearerPrefix = "bearer "

	// healthCheckMethod is the one gRPC method served without a token, so
	// container orchestrators can probe liveness. It reports SERVING or not
	// and nothing else.
	healthCheckMethod = "/grpc.health.v1.Health/Check"
)

// ResolveToken returns the API token configured through token (inline, from
// the environment or a flag) or tokenFile (a path whose trimmed contents are
// the token). It returns "" and no error when neither is set. Setting both is
// an error: two sources would make it unclear which one is enforced.
func ResolveToken(token, tokenFile string) (string, error) {
	token = strings.TrimSpace(token)
	switch {
	case token != "" && tokenFile != "":
		ce := conduiterr.New(CodeTokenInvalid, "both api.token and api.token-file are set")
		ce.ConfigPath = ConfigPathTokenFile
		ce.Suggestion = "set only one of api.token (CONDUIT_API_TOKEN) and api.token-file (CONDUIT_API_TOKEN_FILE); prefer the file"
		return "", ce
	case tokenFile != "":
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			// The path is the operator's own config, not a secret; the file's
			// contents never appear in any message.
			ce := conduiterr.Wrap(CodeTokenInvalid, "could not read api.token-file", err)
			ce.ConfigPath = ConfigPathTokenFile
			ce.Suggestion = "check that the file exists and is readable by the Conduit process"
			return "", ce
		}
		token = strings.TrimSpace(string(b))
		if token == "" {
			ce := conduiterr.New(CodeTokenInvalid, "api.token-file is empty")
			ce.ConfigPath = ConfigPathTokenFile
			ce.Suggestion = "put a random token of at least 16 characters in the file, for example the output of `openssl rand -hex 32`"
			return "", ce
		}
	}
	if token == "" {
		return "", nil
	}
	if len(token) < MinTokenLength {
		ce := conduiterr.New(CodeTokenInvalid, "the API token is shorter than the 16 character minimum")
		ce.ConfigPath = ConfigPathToken
		ce.Suggestion = "use a random token of at least 16 characters, for example the output of `openssl rand -hex 32`"
		return "", ce
	}
	return token, nil
}

// tokensEqual compares two tokens in constant time. Both are hashed first so
// the comparison time does not depend on the length of either input.
func tokensEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// bearerToken extracts the token from an Authorization value. The scheme is
// case-insensitive (RFC 7235).
func bearerToken(authz string) (string, bool) {
	if len(authz) < len(bearerPrefix) || !strings.EqualFold(authz[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	return strings.TrimSpace(authz[len(bearerPrefix):]), true
}

// Authenticator enforces a single bearer token.
type Authenticator struct {
	token  string
	logger log.CtxLogger
}

// NewAuthenticator returns an Authenticator for token. token must be
// non-empty; callers decide whether authentication is enabled at all.
func NewAuthenticator(token string, logger log.CtxLogger) *Authenticator {
	return &Authenticator{token: token, logger: logger.WithComponent("apisec.Authenticator")}
}

func unauthenticatedError() *conduiterr.ConduitError {
	ce := conduiterr.New(CodeUnauthenticated, "missing or invalid API token")
	ce.ConfigPath = ConfigPathToken
	ce.Suggestion = "send `Authorization: Bearer <token>`; the CLI reads CONDUIT_API_TOKEN, CONDUIT_API_TOKEN_FILE or --api.token-file"
	return ce
}

// HTTP wraps next so that requests without the configured bearer token are
// rejected with 401. Exempt requests skip the check; they must be matched on
// exact, uncleaned values so a crafted path cannot borrow an exemption.
func (a *Authenticator) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if httpExempt(r) {
			next.ServeHTTP(w, r)
			return
		}
		presented, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok || !tokensEqual(presented, a.token) {
			// Never log the presented credential.
			a.logger.Warn(r.Context()).Str("remote_addr", r.RemoteAddr).Str("method", r.Method).
				Msg("rejected HTTP request: missing or invalid API token")
			w.Header().Set("WWW-Authenticate", `Bearer realm="conduit"`)
			writeHTTPError(w, http.StatusUnauthorized, unauthenticatedError())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// httpExempt reports whether r is served without a token: the liveness and
// readiness probes, and CORS preflights (browsers never attach credentials to
// a preflight; it carries no data and the real request is still gated).
func httpExempt(r *http.Request) bool {
	if r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
		return true
	}
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

func (a *Authenticator) checkGRPC(ctx context.Context, fullMethod string) error {
	if fullMethod == healthCheckMethod {
		return nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	for _, v := range md.Get("authorization") {
		if presented, ok := bearerToken(v); ok && tokensEqual(presented, a.token) {
			return nil
		}
	}
	a.logger.Warn(ctx).Str("method", fullMethod).Msg("rejected gRPC request: missing or invalid API token")
	return conduiterr.ToStatus(unauthenticatedError()).Err()
}

// UnaryInterceptor rejects unary calls without the configured token.
func (a *Authenticator) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if err := a.checkGRPC(ctx, info.FullMethod); err != nil {
			return nil, err
		}
		return h(ctx, req)
	}
}

// StreamInterceptor rejects streaming calls (including server reflection)
// without the configured token.
func (a *Authenticator) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		if err := a.checkGRPC(ss.Context(), info.FullMethod); err != nil {
			return err
		}
		return h(srv, ss)
	}
}

// BearerCredentials returns per-RPC credentials that attach token to every
// call. Used by Conduit's own HTTP gateway, which authenticates the HTTP
// request first and then calls the in-process gRPC server, and by the CLI.
// Transport security is not required: Conduit serves plaintext gRPC and
// terminates TLS, where wanted, in front of it.
func BearerCredentials(token string) credentials.PerRPCCredentials {
	return bearerCreds{token: token}
}

type bearerCreds struct{ token string }

func (c bearerCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + c.token}, nil
}

func (bearerCreds) RequireTransportSecurity() bool { return false }
