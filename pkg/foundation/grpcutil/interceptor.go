// Copyright © 2022 Meroxa, Inc.
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

package grpcutil

import (
	"context"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// RequestIDUnaryServerInterceptor tries to fetch the request ID from metadata
// and generates a new request ID if not found. It also adds the request ID to
// the response metadata.
func RequestIDUnaryServerInterceptor(logger log.CtxLogger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp interface{}, err error) {
		var requestID string

		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			// this shouldn't ever happen, if it does we recover anyway
			logger.Warn(ctx).Msg("could not find GRPC metadata in incoming context, creating empty metadata")
			md = make(metadata.MD)
			ctx = metadata.NewIncomingContext(ctx, md)
		}

		header := md.Get(RequestIDHeader)
		if len(header) > 0 {
			requestID = strings.Trim(header[0], " ")
		}

		if requestID == "" {
			requestID = uuid.NewString()
			logger.Trace(ctx).Str(log.RequestIDField, requestID).Msg("generated request ID")
		}

		ctx = ctxutil.ContextWithRequestID(ctx, requestID)
		err = grpc.SetHeader(ctx, metadata.Pairs(RequestIDHeader, requestID))
		if err != nil {
			// only display a warning but continue processing the request
			logger.Warn(ctx).Err(err).Msgf("could not set header %q", RequestIDHeader)
		}
		return handler(ctx, req)
	}
}

// LoggerUnaryServerInterceptor logs all GRPC requests when they are returned.
// It logs the duration, grpc method and grpc status code. If the request
// originated from the GRPC gateway (HTTP request) the HTTP endpoint is logged
// as well, assuming the GRPC gateway has the WithHTTPEndpointHeader middleware.
func LoggerUnaryServerInterceptor(logger log.CtxLogger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp interface{}, err error) {
		var httpEndpoint string
		md, ok := metadata.FromIncomingContext(ctx)
		if ok {
			header := md.Get(HTTPEndpointHeader)
			if len(header) > 0 {
				httpEndpoint = header[0]
			}
		}
		start := time.Now()
		defer func() {
			duration := time.Since(start)
			e := logger.Err(ctx, err)
			// set logger level to trace if it's a healthcheck request and has no error
			if info.FullMethod == "/grpc.health.v1.Health/Check" && err == nil {
				e = logger.Trace(ctx)
			}

			if httpEndpoint != "" {
				// request was forwarded by GRPC gateway, output the endpoint
				e = e.Str(log.HTTPEndpointField, httpEndpoint)
			}
			e.Str(log.GRPCMethodField, info.FullMethod).
				Dur(log.DurationField, duration).
				Str(log.GRPCStatusCodeField, status.Code(err).String()).
				Msg("request processed")
		}()
		return handler(ctx, req)
	}
}
