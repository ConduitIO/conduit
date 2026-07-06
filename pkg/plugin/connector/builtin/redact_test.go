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

package builtin

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/mock"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

// Sentinel secret values used across this file's tests. If either ever shows
// up in a captured log buffer, a leak site regressed.
const (
	redactTestURLSentinel = "postgres://u:S3CR3T_SENTINEL_9f3a@h/db"
	redactTestPwSentinel  = "PW_SENTINEL_9f3a"
)

func redactTestConfig() config.Config {
	return config.Config{
		"url":           redactTestURLSentinel,
		"sasl.password": redactTestPwSentinel,
	}
}

// assertNoSecretLeak is the shared assertion for every test below: sentinels
// must never appear in the captured log output, the redacted marker must
// appear, and config keys must remain visible.
func assertNoSecretLeak(is *is.I, buf *bytes.Buffer) {
	got := buf.String()
	is.True(!strings.Contains(got, redactTestURLSentinel)) // url sentinel leaked into log output
	is.True(!strings.Contains(got, redactTestPwSentinel))  // password sentinel leaked into log output
	is.True(strings.Contains(got, log.Redacted))           // redacted marker missing from log output
	is.True(strings.Contains(got, `"url"`))                // key should stay visible
	is.True(strings.Contains(got, `"sasl.password"`))      // key should stay visible
}

// TestSourcePluginAdapter_DoesNotLeakSecrets is the secrets-redaction
// regression test (v0.16.1 execution plan §1.1) for the source adapter leak
// sites fixed in source.go: Configure, LifecycleOnCreated, LifecycleOnUpdated
// and LifecycleOnDeleted used to log the entire incoming pconnector request
// (including Config/ConfigBefore/ConfigAfter) via Any("request", in). Each
// case here drives the real adapter method with a secret-fixture config and
// asserts the sentinel values never reach the log buffer.
func TestSourcePluginAdapter_DoesNotLeakSecrets(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	mockSource := mock.NewSourcePlugin(ctrl)
	mockSource.EXPECT().Configure(gomock.Any(), gomock.Any()).Return(pconnector.SourceConfigureResponse{}, nil).AnyTimes()
	mockSource.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil).AnyTimes()
	mockSource.EXPECT().LifecycleOnUpdated(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnUpdatedResponse{}, nil).AnyTimes()
	mockSource.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnDeletedResponse{}, nil).AnyTimes()

	cfg := redactTestConfig()

	testCases := []struct {
		name string
		call func(*sourcePluginAdapter) error
	}{
		{
			name: "Configure",
			call: func(a *sourcePluginAdapter) error {
				_, err := a.Configure(ctx, pconnector.SourceConfigureRequest{Config: cfg})
				return err
			},
		},
		{
			name: "LifecycleOnCreated",
			call: func(a *sourcePluginAdapter) error {
				_, err := a.LifecycleOnCreated(ctx, pconnector.SourceLifecycleOnCreatedRequest{Config: cfg})
				return err
			},
		},
		{
			name: "LifecycleOnUpdated",
			call: func(a *sourcePluginAdapter) error {
				_, err := a.LifecycleOnUpdated(ctx, pconnector.SourceLifecycleOnUpdatedRequest{ConfigBefore: cfg, ConfigAfter: cfg})
				return err
			},
		},
		{
			name: "LifecycleOnDeleted",
			call: func(a *sourcePluginAdapter) error {
				_, err := a.LifecycleOnDeleted(ctx, pconnector.SourceLifecycleOnDeletedRequest{Config: cfg})
				return err
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			var buf bytes.Buffer
			logger := log.New(zerolog.New(&buf).Level(zerolog.DebugLevel))
			adapter := newSourcePluginAdapter(mockSource, logger)

			err := tc.call(adapter)
			is.NoErr(err)

			assertNoSecretLeak(is, &buf)
		})
	}
}

// TestDestinationPluginAdapter_DoesNotLeakSecrets mirrors
// TestSourcePluginAdapter_DoesNotLeakSecrets for destination.go's adapter.
func TestDestinationPluginAdapter_DoesNotLeakSecrets(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	mockDestination := mock.NewDestinationPlugin(ctrl)
	mockDestination.EXPECT().Configure(gomock.Any(), gomock.Any()).Return(pconnector.DestinationConfigureResponse{}, nil).AnyTimes()
	mockDestination.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).Return(pconnector.DestinationLifecycleOnCreatedResponse{}, nil).AnyTimes()
	mockDestination.EXPECT().LifecycleOnUpdated(gomock.Any(), gomock.Any()).Return(pconnector.DestinationLifecycleOnUpdatedResponse{}, nil).AnyTimes()
	mockDestination.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).Return(pconnector.DestinationLifecycleOnDeletedResponse{}, nil).AnyTimes()

	cfg := redactTestConfig()

	testCases := []struct {
		name string
		call func(*destinationPluginAdapter) error
	}{
		{
			name: "Configure",
			call: func(a *destinationPluginAdapter) error {
				_, err := a.Configure(ctx, pconnector.DestinationConfigureRequest{Config: cfg})
				return err
			},
		},
		{
			name: "LifecycleOnCreated",
			call: func(a *destinationPluginAdapter) error {
				_, err := a.LifecycleOnCreated(ctx, pconnector.DestinationLifecycleOnCreatedRequest{Config: cfg})
				return err
			},
		},
		{
			name: "LifecycleOnUpdated",
			call: func(a *destinationPluginAdapter) error {
				_, err := a.LifecycleOnUpdated(ctx, pconnector.DestinationLifecycleOnUpdatedRequest{ConfigBefore: cfg, ConfigAfter: cfg})
				return err
			},
		},
		{
			name: "LifecycleOnDeleted",
			call: func(a *destinationPluginAdapter) error {
				_, err := a.LifecycleOnDeleted(ctx, pconnector.DestinationLifecycleOnDeletedRequest{Config: cfg})
				return err
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			var buf bytes.Buffer
			logger := log.New(zerolog.New(&buf).Level(zerolog.DebugLevel))
			adapter := newDestinationPluginAdapter(mockDestination, logger)

			err := tc.call(adapter)
			is.NoErr(err)

			assertNoSecretLeak(is, &buf)
		})
	}
}

// TestReturnResponse_DetachedContext_DoesNotLeakResponseValue exercises
// sandbox.go's returnResponse on the ctx-cancelled path (the call comes from
// a detached goroutine after the caller stopped waiting - see runSandbox).
// The response value used to be logged in full via Any("response", res); it
// is now reduced to its type name. This test doesn't use a secret directly
// (response types in this protocol version don't carry Config - only
// requests do), but it does confirm the generic res value's content, whatever
// it is, is never serialized into the log, only its %T type name.
func TestReturnResponse_DetachedContext_DoesNotLeakResponseValue(t *testing.T) {
	is := is.New(t)
	var buf bytes.Buffer
	logger := log.New(zerolog.New(&buf).Level(zerolog.DebugLevel))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // ctx is already done before returnResponse runs

	type sensitiveResponse struct {
		Secret string
	}
	res := sensitiveResponse{Secret: redactTestPwSentinel}

	// c is unbuffered and nobody reads from it, so the ctx.Done() branch is
	// the only one that can complete.
	c := make(chan any)
	returnResponse(ctx, res, nil, c, logger)

	got := buf.String()
	is.True(!strings.Contains(got, redactTestPwSentinel))                         // response content leaked into log output
	is.True(strings.Contains(got, "sensitiveResponse"))                           // type name should still be logged
	is.True(strings.Contains(got, `"response_type":"builtin.sensitiveResponse"`)) // exact field shape
}
