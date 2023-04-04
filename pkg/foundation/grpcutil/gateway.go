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
	"net/http"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/uuid"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/protobuf/encoding/protojson"
)

const mimeApplicationJSONPretty = "application/json+pretty"

// WithPrettyJSONMarshaler returns a GRPC gateway ServeMuxOption which prints a
// pretty JSON output when Accept header contains application/json+pretty.
func WithPrettyJSONMarshaler() runtime.ServeMuxOption {
	return runtime.WithMarshalerOption(mimeApplicationJSONPretty, &runtime.JSONPb{
		MarshalOptions: protojson.MarshalOptions{
			Indent:          "  ",
			Multiline:       true, // optional, implied by presence of "Indent"
			EmitUnpopulated: true, // default marshaller contains this option
		},
		UnmarshalOptions: protojson.UnmarshalOptions{
			DiscardUnknown: true,
		},
	})
}

// WithErrorHandler makes sure that we log any requests that errored out and add
// a request ID to the response.
func WithErrorHandler(logger log.CtxLogger) runtime.ServeMuxOption {
	return runtime.WithErrorHandler(
		func(
			ctx context.Context,
			mux *runtime.ServeMux,
			marshaler runtime.Marshaler,
			w http.ResponseWriter,
			r *http.Request,
			err error,
		) {
			// The context does not contain a request ID or the HTTP endpoint,
			// all of this is added in GRPC interceptors, take it out manually.
			endpoint := extractEndpoint(r)
			requestID := r.Header.Get(RequestIDHeader)
			if requestID == "" {
				requestID = uuid.NewString()
				logger.Trace(ctx).Str(log.RequestIDField, requestID).Msg("generated request ID")
			}
			w.Header().Set(RequestIDHeader, requestID)

			logger.
				Err(ctx, err).
				Str(log.RequestIDField, requestID).
				Str(log.HTTPEndpointField, endpoint).
				Msg("error processing HTTP request")

			runtime.DefaultHTTPErrorHandler(ctx, mux, marshaler, w, r, err)
		},
	)
}

// WithDefaultGatewayMiddleware wraps the handler with the default GRPC Gateway
// middleware.
func WithDefaultGatewayMiddleware(h http.Handler) http.Handler {
	middleware := []func(http.Handler) http.Handler{
		WithPrettyJSONHeader,
		WithHTTPEndpointHeader,
	}
	// start wrapping handler with middleware from last to first, so that they
	// are called in the right order
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}

func WithPrettyJSONHeader(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// add header application/json+pretty when URL parameter "pretty" is supplied
		// checking Values as map[string][]string also catches ?pretty and ?pretty=
		// r.URL.Query().Get("pretty") would not.
		if _, ok := r.URL.Query()["pretty"]; ok {
			r.Header.Set("Accept", mimeApplicationJSONPretty)
		}
		h.ServeHTTP(w, r)
	})
}

func WithHTTPEndpointHeader(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// add header to indicate this request has gone through GRPC gateway
		r.Header.Set(HTTPEndpointHeader, extractEndpoint(r))
		h.ServeHTTP(w, r)
	})
}

func WithWebsockets(ctx context.Context, h http.Handler, l log.CtxLogger) http.Handler {
	return newWebSocketProxy(ctx, h, l)
}

func extractEndpoint(r *http.Request) string {
	return r.Method + " " + r.URL.Path
}
