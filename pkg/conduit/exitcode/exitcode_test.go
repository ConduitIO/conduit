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

package exitcode_test

import (
	"context"
	"syscall"
	"testing"

	"github.com/conduitio/conduit/pkg/conduit/exitcode"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"

	// Blank-imported so their init-time conduiterr.Register calls populate
	// the registry that TestExitCode_RegistryCompleteness iterates. Without
	// these, conduiterr.Codes() would only see the codes conduiterr itself
	// registers (the foundational ones plus CodeUnavailable), since Go only
	// runs a package's init on packages actually reachable from this test
	// binary's import graph.
	_ "github.com/conduitio/conduit/pkg/connector"
	_ "github.com/conduitio/conduit/pkg/orchestrator"
	_ "github.com/conduitio/conduit/pkg/pipeline"
	_ "github.com/conduitio/conduit/pkg/processor"
	_ "github.com/conduitio/conduit/pkg/provisioning/config"

	"github.com/matryer/is"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// testCodes registers one conduiterr.Code per gRPC category this package
// classifies, independent of whatever happens to be registered by
// production packages at the time this test runs. This keeps
// TestExitCode_ByGRPCCategory a pure test of the classifier's category ->
// bucket mapping, not coupled to which production reason happens to use
// which category today.
//
// codes.Canceled is deliberately not registered here: no real boundary would
// ever register a domain error code in the Canceled category (a "canceled"
// ConduitError is a contradiction — Canceled means the operation was
// deliberately aborted, not that something went wrong), and registering one
// here would pollute TestExitCode_RegistryCompleteness, whose whole point is
// that every *registered* code is definitionally an error and must never
// classify as OK. Canceled is still fully covered below via the raw-status
// path, which is how it actually reaches ExitCode in practice.
var testCodes = map[codes.Code]conduiterr.Code{
	codes.Unknown:            conduiterr.Register("test.exitcode.unknown", codes.Unknown),
	codes.InvalidArgument:    conduiterr.Register("test.exitcode.invalid_argument", codes.InvalidArgument),
	codes.DeadlineExceeded:   conduiterr.Register("test.exitcode.deadline_exceeded", codes.DeadlineExceeded),
	codes.NotFound:           conduiterr.Register("test.exitcode.not_found", codes.NotFound),
	codes.AlreadyExists:      conduiterr.Register("test.exitcode.already_exists", codes.AlreadyExists),
	codes.PermissionDenied:   conduiterr.Register("test.exitcode.permission_denied", codes.PermissionDenied),
	codes.ResourceExhausted:  conduiterr.Register("test.exitcode.resource_exhausted", codes.ResourceExhausted),
	codes.FailedPrecondition: conduiterr.Register("test.exitcode.failed_precondition", codes.FailedPrecondition),
	codes.Aborted:            conduiterr.Register("test.exitcode.aborted", codes.Aborted),
	codes.OutOfRange:         conduiterr.Register("test.exitcode.out_of_range", codes.OutOfRange),
	codes.Unimplemented:      conduiterr.Register("test.exitcode.unimplemented", codes.Unimplemented),
	codes.Internal:           conduiterr.Register("test.exitcode.internal", codes.Internal),
	codes.Unavailable:        conduiterr.Register("test.exitcode.unavailable", codes.Unavailable),
	codes.DataLoss:           conduiterr.Register("test.exitcode.data_loss", codes.DataLoss),
	codes.Unauthenticated:    conduiterr.Register("test.exitcode.unauthenticated", codes.Unauthenticated),
}

func TestExitCode_NilAndCanceled(t *testing.T) {
	is := is.New(t)

	is.Equal(exitcode.ExitCode(nil), exitcode.OK)
	is.Equal(exitcode.ExitCode(context.Canceled), exitcode.OK)
	// Canceled must still resolve to OK when wrapped, since that's how it
	// reaches ExitCode in practice (entrypoint.go wraps the tomb's error).
	is.Equal(exitcode.ExitCode(cerrors.Errorf("run: %w", context.Canceled)), exitcode.OK)
}

func TestExitCode_PlainError(t *testing.T) {
	is := is.New(t)
	is.Equal(exitcode.ExitCode(cerrors.New("boom")), exitcode.Runtime)
}

// TestExitCode_ByGRPCCategory is the per-bucket table test required by the
// exit-code design: every gRPC category the classifier knows about, both as
// a *conduiterr.ConduitError and as a raw gRPC client error (the shape a
// server-raised ConduitError actually takes once it crosses the wire, since
// no client call site decodes the ErrorInfo detail back into a ConduitError
// today).
func TestExitCode_ByGRPCCategory(t *testing.T) {
	tests := []struct {
		code codes.Code
		want int
	}{
		{codes.Canceled, exitcode.OK},
		{codes.InvalidArgument, exitcode.Validation},
		{codes.NotFound, exitcode.Validation},
		{codes.AlreadyExists, exitcode.Validation},
		{codes.FailedPrecondition, exitcode.Validation},
		{codes.OutOfRange, exitcode.Validation},
		{codes.Internal, exitcode.Runtime},
		{codes.Unknown, exitcode.Runtime},
		{codes.DataLoss, exitcode.Runtime},
		{codes.Aborted, exitcode.Runtime},
		{codes.Unimplemented, exitcode.Runtime},
		{codes.Unavailable, exitcode.Environment},
		{codes.DeadlineExceeded, exitcode.Environment},
		{codes.ResourceExhausted, exitcode.Environment},
		{codes.Unauthenticated, exitcode.Environment},
		{codes.PermissionDenied, exitcode.Environment},
	}

	for _, tt := range tests {
		t.Run(tt.code.String(), func(t *testing.T) {
			is := is.New(t)

			// codes.Canceled has no registered conduiterr.Code (see testCodes'
			// doc); exercise it only via the raw-status path below.
			if tt.code != codes.Canceled {
				ce := conduiterr.New(testCodes[tt.code], "test error")
				is.Equal(exitcode.ExitCode(ce), tt.want)

				// A ConduitError surviving several layers of ordinary wrapping
				// (cerrors.Errorf %w, as every real boundary in this codebase
				// does) must still classify the same way.
				wrapped := cerrors.Errorf("outer: %w", cerrors.Errorf("inner: %w", ce))
				is.Equal(exitcode.ExitCode(wrapped), tt.want)
			}

			// The raw gRPC client-error shape: no ConduitError anywhere in
			// the chain, only the top-level status code (what a CLI command
			// actually receives back from a grpc.ClientConn call today).
			rawStatus := grpcstatus.New(tt.code, "test error").Err()
			is.Equal(exitcode.ExitCode(rawStatus), tt.want)
		})
	}
}

// TestExitCode_RegistryCompleteness guards every registered conduiterr.Code
// against ever being un-classifiable: whatever gRPC category a boundary
// registers a new code with, ExitCode must resolve it to exactly one of
// {Runtime, Validation, Environment} (never OK — a registered code is
// definitionally an error, so it must never silently exit 0).
func TestExitCode_RegistryCompleteness(t *testing.T) {
	is := is.New(t)

	valid := map[int]bool{exitcode.Runtime: true, exitcode.Validation: true, exitcode.Environment: true}

	codesList := conduiterr.Codes()
	is.True(len(codesList) > 0) // sanity: the registry isn't empty in this test binary

	for _, c := range codesList {
		got := exitcode.ExitCode(conduiterr.New(c, "test error"))
		if !valid[got] {
			t.Errorf("registered code %q (gRPC %s) maps to exit code %d, want one of {%d,%d,%d}",
				c.Reason(), c.GRPCCode(), got, exitcode.Runtime, exitcode.Validation, exitcode.Environment)
		}
	}
}

func TestExitCode_EnvironmentSentinel(t *testing.T) {
	is := is.New(t)

	is.Equal(exitcode.ExitCode(syscall.ECONNREFUSED), exitcode.Environment)
	is.Equal(exitcode.ExitCode(syscall.EADDRINUSE), exitcode.Environment)
	// Still classifies correctly wrapped, the same way a real dial/listen
	// failure reaches ExitCode after cerrors.Errorf %w context.
	is.Equal(exitcode.ExitCode(cerrors.Errorf("dial: %w", syscall.ECONNREFUSED)), exitcode.Environment)
}
