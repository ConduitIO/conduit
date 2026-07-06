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

package api

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	grpcstatus "google.golang.org/grpc/status"
)

// fakeHealthClient lets CheckHealth be exercised without a real gRPC server.
// Only Check is used by CheckHealth; List/Watch are unused stubs that
// satisfy healthgrpc.HealthClient structurally.
type fakeHealthClient struct {
	resp *healthgrpc.HealthCheckResponse
	err  error
}

func (f fakeHealthClient) Check(context.Context, *healthgrpc.HealthCheckRequest, ...grpc.CallOption) (*healthgrpc.HealthCheckResponse, error) {
	return f.resp, f.err
}

func (f fakeHealthClient) List(context.Context, *healthgrpc.HealthListRequest, ...grpc.CallOption) (*healthgrpc.HealthListResponse, error) {
	return nil, cerrors.New("not implemented in fakeHealthClient")
}

func (f fakeHealthClient) Watch(context.Context, *healthgrpc.HealthCheckRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[healthgrpc.HealthCheckResponse], error) {
	return nil, cerrors.New("not implemented in fakeHealthClient")
}

// TestClient_CheckHealth_Serving is the trivial success path: no error, no
// tagging.
func TestClient_CheckHealth_Serving(t *testing.T) {
	is := is.New(t)
	c := &Client{HealthClient: fakeHealthClient{resp: &healthgrpc.HealthCheckResponse{Status: healthgrpc.HealthCheckResponse_SERVING}}}

	err := c.CheckHealth(context.Background(), ":8084")
	is.NoErr(err)
}

// TestClient_CheckHealth_Unreachable covers both response shapes
// CheckHealth tags conduiterr.CodeUnavailable for (so pkg/conduit/exitcode
// classifies either as an environment failure, exit code 3, instead of the
// generic runtime bucket), and both must carry a stack trace.
//
// The "transport error" case is also the regression test for an
// adversarial-self-review finding caught before this landed: an earlier
// version of this fix built the error message via
// cerrors.Errorf(...).Error() (which captures a stack frame) but then
// discarded that frame-carrying error and passed the raw err to
// conduiterr.Wrap as the cause instead. conduiterr.Wrap only synthesizes a
// frame of its own when its cause argument is nil, so a non-nil, frame-less
// cause (a real gRPC transport error, unlike a cerrors-constructed one,
// carries no frame) silently produced a traceless ConduitError. The
// "non-SERVING response" case doesn't exercise that specific bug (its cause
// is nil either way, which Wrap always covers), but is kept for basic
// coverage of that response shape.
func TestClient_CheckHealth_Unreachable(t *testing.T) {
	tests := []struct {
		name string
		hc   fakeHealthClient
	}{
		{
			// A real gRPC transport error (what HealthClient.Check actually
			// returns on a connection failure) carries no xerrors frame of
			// its own — unlike cerrors.New/Errorf, which always do. Using a
			// frame-less error here is what makes this case able to catch
			// the discarded-frame bug at all: a cerrors-constructed fake
			// would carry a frame regardless of whether Wrap's cause
			// handling is correct.
			name: "transport error",
			hc:   fakeHealthClient{err: grpcstatus.New(codes.Unavailable, "connection refused").Err()},
		},
		{
			name: "non-SERVING response, no transport error",
			hc:   fakeHealthClient{resp: &healthgrpc.HealthCheckResponse{Status: healthgrpc.HealthCheckResponse_NOT_SERVING}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			c := &Client{HealthClient: tt.hc}

			err := c.CheckHealth(context.Background(), ":8084")
			is.True(err != nil)

			ce, ok := conduiterr.Get(err)
			is.True(ok) // CheckHealth must return a *conduiterr.ConduitError
			is.Equal(ce.Code, conduiterr.CodeUnavailable)

			frames, _ := cerrors.GetStackTrace(err).([]cerrors.Frame)
			is.True(len(frames) > 0) // must carry a stack frame, not silently trace-less
		})
	}
}
