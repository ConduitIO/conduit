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

package connector

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Connector error codes. Every error carries one of these codes plus a
// suggested fix, so an API, MCP, or UI consumer knows what happened without
// parsing message text.
var (
	// CodeConnectorNotFound is raised when a referenced connector instance
	// cannot be located.
	CodeConnectorNotFound = conduiterr.Register("connector.instance_not_found", codes.NotFound)
	// CodeConnectorRunning is raised when an operation requires the
	// connector to not be running, but a connector instance for it already
	// exists.
	CodeConnectorRunning = conduiterr.Register("connector.running", codes.FailedPrecondition)
	// CodeConnectorInvalidType is raised when a connector type is neither
	// "source" nor "destination".
	CodeConnectorInvalidType = conduiterr.Register("connector.invalid_type", codes.InvalidArgument)
	// CodeConnectorStatePersistFailed is raised when a batch of connector
	// state writes (source positions, last active config) could not be
	// committed to the store. Nothing in the batch was committed and every
	// connector in it receives this error; no source acked the affected
	// positions upstream, so a restart re-reads from the last stored position.
	CodeConnectorStatePersistFailed = conduiterr.Register("connector.state_persist_failed", codes.Unavailable)
)

// statePersistSuggestion is the Suggestion carried by
// CodeConnectorStatePersistFailed errors.
const statePersistSuggestion = "nothing was acknowledged upstream for the failed writes; fix the store error " +
	"named in the message and restart the pipeline, which resumes from its last stored position " +
	"(see docs/operations/connector-state-write-failures.md)"
