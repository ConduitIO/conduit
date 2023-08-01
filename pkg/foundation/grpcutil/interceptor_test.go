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
	"encoding/json"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func TestRequestIDUnaryServerInterceptor_WithRequestIDHeader(t *testing.T) {
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
			assert.Equal(t, requestID, gotRequestID)
			assert.Equal(t, nil, req)
			return want, nil
		},
	)

	assert.Ok(t, err)
	assert.Equal(t, want, got)
	assert.True(t, handlerIsCalled, "expected handler to be called")
}

func TestRequestIDUnaryServerInterceptor_GenerateRequestID(t *testing.T) {
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
			assert.True(t, gotRequestID != "", "request id should not be empty")
			assert.Equal(t, nil, req)
			return want, nil
		},
	)

	assert.Ok(t, err)
	assert.Equal(t, want, got)
	assert.True(t, handlerIsCalled, "expected handler to be called")
}

func TestLoggerUnaryServerInterceptor(t *testing.T) {
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
			assert.Equal(t, incomingCtx, ctx)
			assert.Equal(t, nil, req)
			return want, nil
		},
	)

	assert.Ok(t, err)
	assert.Equal(t, want, got)
	assert.True(t, handlerIsCalled, "expected handler to be called")

	var gotLog map[string]interface{}
	err = json.Unmarshal(logOutput.Bytes(), &gotLog)
	assert.Ok(t, err)

	wantLog := map[string]interface{}{
		"level":                 "info",
		"duration":              gotLog["duration"], // can't know duration in advance
		"message":               "request processed",
		log.RequestIDField:      requestID,
		log.GRPCMethodField:     fullMethod,
		log.GRPCStatusCodeField: codes.OK.String(),
		log.HTTPEndpointField:   httpEndpoint,
	}

	assert.Equal(t, wantLog, gotLog)
}
