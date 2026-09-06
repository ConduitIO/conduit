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

package pipelines

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeDestinationExists is raised by `pipelines init` when the resolved
// pipeline file already exists and --force was not passed. This is the fix
// for the command's previous behavior: os.OpenFile with O_TRUNC and no
// existence check, which silently overwrote an existing pipeline
// configuration. --force opts into the overwrite; --dry-run never touches
// the filesystem and is exempt from this check entirely (see
// InitCommand.checkDestination).
//
// codes.AlreadyExists classifies to exitcode.Validation (2) via
// pkg/conduit/exitcode, the same bucket as pkg/scaffold's analogous
// CodeDestinationExists for connector/processor scaffolding.
var CodeDestinationExists = conduiterr.Register("pipelines.init_destination_exists", codes.AlreadyExists)

// CodePipelinesPathUnwritable is raised by `pipelines init` when the
// *shape or accessibility of the destination path* prevents the write:
// something that is not a directory already occupies the path, a path
// component is unreadable or unwritable, the pipeline file path is itself a
// directory, or a parent directory does not exist (which is what a pipeline
// name containing a path separator produces). It is scoped by
// isDestinationShapedError in init.go — deliberately NOT every failure the
// create/open can return. A full device, a quota, or an I/O error is not a
// destination problem and stays on conduiterr.CodeInternal (exit 1), so that
// "disk full" cannot classify differently depending on whether the
// filesystem reports ENOSPC at open (macOS) or at write (Linux).
//
// "The pipeline file already exists" is CodeDestinationExists, not this.
//
// It replaces the generic conduiterr.CodeInternal ("internal.error", exit 1)
// this path used to return for everything. internal.error is the
// unclassified-bug bucket; an unwritable destination is an ordinary,
// user-fixable condition, and reporting it as a bug both misleads the user
// and makes the CLI's exit code useless for branching.
//
// codes.FailedPrecondition classifies to exitcode.Validation (2), not
// Environment (3): in this codebase Environment means a dependency Conduit
// needs is unreachable (the server, the database, a bound listen address),
// whereas the destination path is part of the request itself — it comes from
// --pipelines.path (default ./pipelines) and the positional pipeline name.
// The remediation is always "point the command somewhere writable, or make
// this path writable", which is caller-side, so Validation is the honest
// bucket even when the underlying OS error is EACCES.
//
// Known inconsistency, deliberately not resolved here: pkg/scaffold
// (`connectors new`) and cmd/conduit/root/initialize (`conduit init`) both
// map an equivalent MkdirAll failure on a user-supplied path to an Internal
// code and exit 1. Aligning all three is worth a follow-up; this command is
// the one a developer-experience audit found, and exit 2 is the correct
// answer for it.
var CodePipelinesPathUnwritable = conduiterr.Register("pipelines.init_path_unwritable", codes.FailedPrecondition)
