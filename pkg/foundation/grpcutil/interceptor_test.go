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
	"bytes"
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func TestRequestIDUnaryServerInterceptor_WithRequestIDHeader(t *testing.T) {
	is := is.New(t)

	requestID := uuid.NewString()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{
		RequestIDHeader: []string{requestID},
	})

	var handlerIsCalled bool
	want := "response"

	interceptor := RequestIDUnaryServerInterceptor(log.Nop())
	got, err := interceptor(
		ctx,
		nil,
		&grpc.UnaryServerInfo{},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			handlerIsCalled = true
			// supplied context should contain request ID
			gotRequestID := ctxutil.RequestIDFromContext(ctx)
			is.Equal(requestID, gotRequestID)
			is.Equal(nil, req)
			return want, nil
		},
	)

	is.NoErr(err)
	is.Equal(want, got)
	is.True(handlerIsCalled) // expected handler to be called
}

func TestRequestIDUnaryServerInterceptor_GenerateRequestID(t *testing.T) {
	is := is.New(t)

	var handlerIsCalled bool
	want := "response"

	interceptor := RequestIDUnaryServerInterceptor(log.Nop())
	got, err := interceptor(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			handlerIsCalled = true
			// supplied context should contain request ID
			gotRequestID := ctxutil.RequestIDFromContext(ctx)
			is.True(gotRequestID != "") // request id should not be empty
			is.Equal(nil, req)
			return want, nil
		},
	)

	is.NoErr(err)
	is.Equal(want, got)
	is.True(handlerIsCalled) // expected handler to be called
}

func TestLoggerUnaryServerInterceptor(t *testing.T) {
	is := is.New(t)

	var logOutput bytes.Buffer
	logger := log.New(zerolog.New(&logOutput))
	logger.Logger = logger.Hook(ctxutil.RequestIDLogCtxHook{})

	requestID := uuid.NewString()
	fullMethod := "/test/method"
	httpEndpoint := "POST /http/test/path"

	incomingCtx := metadata.NewIncomingContext(context.Background(), metadata.MD{
		HTTPEndpointHeader: []string{httpEndpoint},
	})
	// add request ID to context
	incomingCtx = ctxutil.ContextWithRequestID(incomingCtx, requestID)

	var handlerIsCalled bool
	want := "response"

	interceptor := LoggerUnaryServerInterceptor(logger)
	got, err := interceptor(
		incomingCtx,
		nil,
		&grpc.UnaryServerInfo{
			FullMethod: fullMethod,
		},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			handlerIsCalled = true
			is.Equal(incomingCtx, ctx)
			is.Equal(nil, req)
			return want, nil
		},
	)

	is.NoErr(err)
	is.Equal(want, got)
	is.True(handlerIsCalled) // expected handler to be called

	var gotLog map[string]interface{}
	err = json.Unmarshal(logOutput.Bytes(), &gotLog)
	is.NoErr(err)

	wantLog := map[string]interface{}{
		"level":                 "info",
		"duration":              gotLog["duration"], // can't know duration in advance
		"message":               "request processed",
		log.RequestIDField:      requestID,
		log.GRPCMethodField:     fullMethod,
		log.GRPCStatusCodeField: codes.OK.String(),
		log.HTTPEndpointField:   httpEndpoint,
	}

	is.Equal(wantLog, gotLog)
}
