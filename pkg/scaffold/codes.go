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

package scaffold

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Scaffold error codes. Every hard failure Generate returns carries one of
// these, so pkg/conduit/exitcode's classification (via the gRPC category
// each Code registers under) and a --json consumer both get a stable
// identity instead of parsing message text. See doc.go's "Exit codes"
// section for the bucket each maps to.
var (
	// CodeToolchainUnavailable is raised when the preflight (Go on PATH at
	// the minimum version, git on PATH, GOPATH/bin writable) fails. Maps to
	// gRPC Unavailable -> exitcode.Environment (3): the fix is "install or
	// upgrade a toolchain component", not "change your command line".
	CodeToolchainUnavailable = conduiterr.Register("scaffold.toolchain_unavailable", codes.Unavailable)

	// CodeInvalidModule is raised when --module is missing (and cannot be
	// prompted for) or does not match the
	// "github.com/<owner>/conduit-(connector|processor)-<name>" shape a
	// template's own setup.sh requires. Maps to gRPC InvalidArgument ->
	// exitcode.Validation (2).
	CodeInvalidModule = conduiterr.Register("scaffold.invalid_module", codes.InvalidArgument)

	// CodeUnsupportedLanguage is raised for --lang values other than "go" —
	// today that's every value, since Go is the only ready target (see the
	// design doc's feasibility verdict; Python is blocked on a connector SDK
	// that does not exist yet). Maps to gRPC InvalidArgument ->
	// exitcode.Validation (2).
	CodeUnsupportedLanguage = conduiterr.Register("scaffold.unsupported_language", codes.InvalidArgument)

	// CodeDestinationExists is raised when the target directory already
	// exists and --force was not given. Maps to gRPC AlreadyExists ->
	// exitcode.Validation (2).
	CodeDestinationExists = conduiterr.Register("scaffold.destination_exists", codes.AlreadyExists)

	// CodeGenerateFailed is raised when installing the code-gen tool or
	// running it fails after files are already on disk (in the temp
	// staging directory — see scaffold.go's atomic-rename discipline, so
	// this never leaves a partial directory at the requested destination).
	// Maps to gRPC Internal -> exitcode.Runtime (1).
	CodeGenerateFailed = conduiterr.Register("scaffold.generate_failed", codes.Internal)

	// CodeBuildFailed is raised when the verified `go build ./...` step
	// fails. Maps to gRPC Internal -> exitcode.Runtime (1), per this
	// package's explicit exit-code routing ("verified go build failure ->
	// 1").
	CodeBuildFailed = conduiterr.Register("scaffold.build_failed", codes.Internal)

	// CodeWriteFailed is raised for internal I/O failures unpacking the
	// embedded template snapshot into the staging directory, or performing
	// the atomic rename into place. These are not user-fixable the way a
	// bad flag or a missing toolchain is; they indicate something is wrong
	// with the local filesystem itself. Maps to gRPC Internal ->
	// exitcode.Runtime (1).
	CodeWriteFailed = conduiterr.Register("scaffold.write_failed", codes.Internal)
)
