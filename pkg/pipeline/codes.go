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

package pipeline

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Pipeline error codes. Every error carries one of these codes plus a
// suggested fix, so an API, MCP, or UI consumer knows what happened without
// parsing message text.
var (
	// CodePipelineNotFound is raised when a referenced pipeline instance
	// cannot be located.
	CodePipelineNotFound = conduiterr.Register("pipeline.instance_not_found", codes.NotFound)
	// CodePipelineRunning is raised when an operation requires the pipeline
	// to be stopped, but it is currently running.
	CodePipelineRunning = conduiterr.Register("pipeline.running", codes.FailedPrecondition)
	// CodePipelineNotRunning is raised when an operation requires the
	// pipeline to be running, but it is currently stopped.
	CodePipelineNotRunning = conduiterr.Register("pipeline.not_running", codes.FailedPrecondition)
	// CodePipelineNameAlreadyExists is raised when a pipeline config's name
	// collides with an existing pipeline's name.
	CodePipelineNameAlreadyExists = conduiterr.Register("pipeline.name_already_exists", codes.AlreadyExists)
	// CodePipelineNameMissing is raised when a pipeline config is missing
	// the required name field.
	CodePipelineNameMissing = conduiterr.Register("pipeline.name_missing", codes.InvalidArgument)
	// CodeShuttingDown is raised when a pipeline start is refused because
	// Conduit has begun shutting down: once shutdown starts, no new pipeline
	// run is started (including automatic recovery restarts), so that every
	// run is drained before positions are flushed and the database is closed.
	// Unavailable: retry against the instance after it restarts.
	CodeShuttingDown = conduiterr.Register("pipeline.shutting_down", codes.Unavailable)
	// CodeStatusPersistFailed is raised when a pipeline's status could not be
	// written to the pipeline store. The in-memory status, which the API, the
	// CLI and the lifecycle read, has already moved; the store still holds
	// the previous status, which is what the next boot will act on. The
	// lifecycle treats the write as a report: a run whose status was not
	// persisted keeps running, and Start and Stop do not return this error.
	// Check the database (/healthz, free disk) and whether position writes
	// are failing too. Unavailable: the store may accept the write later.
	CodeStatusPersistFailed = conduiterr.Register("pipeline.status_persist_failed", codes.Unavailable)
)
