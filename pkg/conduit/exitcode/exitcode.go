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

// Package exitcode computes Conduit's deterministic CLI process exit code
// from an error. It is the single source of truth for the mapping documented
// in the README and in docs/architecture-decision-records — every place that
// calls os.Exit for a command or the `run` entrypoint goes through ExitCode,
// so a one-shot command (`cmd/conduit/cli`) and the long-running server
// (`pkg/conduit/entrypoint.go`) can never silently drift onto different exit
// conventions.
//
// # Exit code buckets
//
//   - 0 (OK) — success, or the run was canceled deliberately (context.Canceled,
//     the graceful-shutdown path on a single SIGINT/SIGTERM).
//   - 1 (Runtime) — an internal or unclassified error: a bug, an unexpected
//     panic-adjacent failure, or any error this package has no more specific
//     classification for. This is also the fallback for a signal that
//     doesn't fit the syscall.Signal-based hard-exit convention (should not
//     happen in practice).
//   - 2 (Validation) — the request or config was rejected: invalid argument,
//     not found, already exists, failed precondition, out of range. The fix
//     is on the caller's side (bad input), not the environment.
//   - 3 (Environment) — Conduit could not reach something it depends on:
//     the server is unreachable, a listen address is already bound, the
//     database can't be dialed, or the call was rate-limited/unauthenticated/
//     timed out. The fix is "check the environment", not "fix your request".
//
// A hard exit on a second termination signal (see entrypoint.go) is a
// special case outside this bucketing: it reports the POSIX 128+signum
// convention (SIGINT -> 130, SIGTERM -> 143) instead, so scripts can tell a
// forced kill from an ordinary classified error.
//
// # Classification order
//
// ExitCode tries, in order: nil or context.Canceled (0); a *conduiterr.ConduitError
// anywhere in err's chain, classified by its registered gRPC category; a raw
// gRPC client error (google.golang.org/grpc/status), classified the same way —
// this covers server-side ConduitErrors that reach a CLI-via-client command as
// a plain status error, since the client never decodes the ErrorInfo detail
// back into a ConduitError today; a narrow set of OS/network-level sentinel
// errors that indicate an environment problem even when nothing wrapped them;
// and finally Runtime (1) as the catch-all.
//
// # Coverage is intentionally partial today
//
// Only the highest-value environment call sites are tagged with
// conduiterr.CodeUnavailable so far (CLI-to-server health check, listen
// address already in use, database unreachable at startup) — see the ADR for
// the rationale. Everywhere else, an untagged error still classifies as
// Runtime (1), which matches today's actual behavior (every entrypoint
// failure exits 1) and is not a regression; coverage grows as more
// boundaries adopt conduiterr, without changing this package's contract.
package exitcode

import (
	"context"
	"syscall"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// Process exit codes. See the package doc for the meaning of each bucket.
const (
	// OK is returned for success and for a deliberately canceled run.
	OK = 0
	// Runtime is returned for internal/unclassified errors, and is the
	// catch-all when no more specific classification applies.
	Runtime = 1
	// Validation is returned when the request or config was rejected.
	Validation = 2
	// Environment is returned when a required external dependency could not
	// be reached.
	Environment = 3
)

// ExitCode classifies err into one of the process exit code buckets
// documented on the package. It is the single classifier shared by one-shot
// CLI commands (cmd/conduit/cli) and the `run` entrypoint
// (pkg/conduit/entrypoint.go), so the two surfaces cannot drift onto
// different conventions for the same underlying error.
//
// ExitCode never panics: every branch has a total fallback, ending in
// Runtime for anything it cannot classify more specifically.
func ExitCode(err error) int {
	if err == nil || cerrors.Is(err, context.Canceled) {
		return OK
	}

	if ce, ok := conduiterr.Get(err); ok {
		return fromGRPCCode(ce.Code.GRPCCode())
	}

	// A ConduitError raised by the server crosses the gRPC boundary as a
	// plain status error on the client: the top-level gRPC code is set
	// explicitly from the registry (conduiterr.ToStatus), but the client
	// stubs don't decode the ErrorInfo detail back into a *ConduitError
	// today (no call site does `conduiterr.FromStatus` yet). status.FromError
	// only reports ok=true for an error that actually carries a gRPC status
	// (nil, or implements the GRPCStatus() interface); a plain Go error
	// correctly falls through to the sentinel check below instead of being
	// misclassified as codes.Unknown.
	if st, ok := grpcstatus.FromError(err); ok {
		return fromGRPCCode(st.Code())
	}

	if isEnvironmentSentinel(err) {
		return Environment
	}

	return Runtime
}

// fromGRPCCode maps a gRPC status category to an exit code bucket. The
// mapping is total: every codes.Code value (including any the switch doesn't
// name explicitly, and codes.OK) falls through to a defined bucket, matching
// TestExitCode_CoversEveryRegisteredCode's assumption that every registered
// conduiterr.Code lands in exactly one of {1, 2, 3}.
func fromGRPCCode(c codes.Code) int {
	switch c {
	case codes.OK, codes.Canceled:
		return OK
	case codes.InvalidArgument, codes.NotFound, codes.AlreadyExists,
		codes.FailedPrecondition, codes.OutOfRange:
		return Validation
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted,
		codes.Unauthenticated, codes.PermissionDenied:
		return Environment
	case codes.Internal, codes.Unknown, codes.DataLoss, codes.Aborted, codes.Unimplemented:
		return Runtime
	default:
		return Runtime
	}
}

// isEnvironmentSentinel reports whether err carries one of a narrow set of
// OS-level connection sentinels that indicate an environment problem even
// when it reaches ExitCode unwrapped by a ConduitError or a gRPC status —
// e.g. a lower-level dependency (not yet migrated to conduiterr tagging)
// that returns a raw *net.OpError. This is deliberately narrow: it is a
// safety net for the same high-value cases the tagged call sites cover
// (server/database unreachable, listen address already bound), not a
// general network-error classifier — a broader net (e.g. matching on
// *net.OpError alone) risks misclassifying an unrelated connector-plugin
// network error as an environment failure for the whole process.
func isEnvironmentSentinel(err error) bool {
	return cerrors.Is(err, syscall.ECONNREFUSED) || cerrors.Is(err, syscall.EADDRINUSE)
}
