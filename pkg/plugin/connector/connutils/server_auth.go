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

package connutils

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/http/apisec"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

const (
	// connectorTokenMetadataKey is the gRPC metadata key the connector SDK sends
	// the connector token under (conduit-connector-protocol, pconnutils).
	connectorTokenMetadataKey = "conduit-connector-token"

	// healthCheckMethod is the one method served without a token: it reports
	// SERVING and nothing else.
	healthCheckMethod = "/grpc.health.v1.Health/Check"
)

func unauthenticatedError() error {
	ce := conduiterr.New(apisec.CodeUnauthenticated, "missing or invalid connector token")
	ce.Suggestion = "this server is only for connector plugins started by Conduit; use the Conduit API (with the API token) instead"
	return conduiterr.ToStatus(ce).Err()
}

// check requires a registered connector token on the call, except for the
// health check.
//
// Invariant: no RPC on the utilities server runs for a caller that did not
// present a token Conduit issued. It is enforced here for the whole server,
// not per handler, so an RPC added later cannot forget it.
func (s *AuthManager) check(ctx context.Context, fullMethod string, rej *apisec.Rejections) error {
	if fullMethod == healthCheckMethod {
		return nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	for _, tkn := range md.Get(connectorTokenMetadataKey) {
		if s.IsTokenValid(tkn) == nil {
			return nil
		}
	}
	remote := ""
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		remote = p.Addr.String()
	}
	// Never log the presented token.
	rej.Record(ctx, apisec.CodeUnauthenticated, remote, "rejected connector utilities request: missing or invalid connector token")
	return unauthenticatedError()
}

// UnaryServerInterceptor rejects unary calls without a valid connector token.
// rej may be nil.
func (s *AuthManager) UnaryServerInterceptor(rej *apisec.Rejections) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if err := s.check(ctx, info.FullMethod, rej); err != nil {
			return nil, err
		}
		return h(ctx, req)
	}
}

// StreamServerInterceptor rejects streaming calls without a valid connector token.
func (s *AuthManager) StreamServerInterceptor(rej *apisec.Rejections) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		if err := s.check(ss.Context(), info.FullMethod, rej); err != nil {
			return err
		}
		return h(srv, ss)
	}
}
