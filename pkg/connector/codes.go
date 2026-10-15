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
	// CodeConnectorStatePersistFailed is raised when a connector's state write
	// (source position, last active config) could not be committed to the
	// store. When one connector's write fails, only that connector receives
	// this error and the rest of the batch is committed. When opening or
	// committing the transaction fails, every connector still in that attempt
	// receives it. A source that receives it has not acked the affected
	// positions upstream, so a restart re-reads from the last stored position.
	CodeConnectorStatePersistFailed = conduiterr.Register("connector.state_persist_failed", codes.Unavailable)
	// CodeConnectorDeleteHookFailed is raised when a connector is deleted and
	// its plugin's delete hook (the "deleted" lifecycle event, which releases
	// what the connector created outside Conduit, such as a Postgres
	// replication slot) fails or cannot be run. The connector record is kept,
	// so the delete can be retried; the hook is idempotent by contract. The
	// message names the connector, its plugin, and the plugin's own error,
	// which is the only place the plugin can say what it failed to release
	// (#3058).
	CodeConnectorDeleteHookFailed = conduiterr.Register("connector.delete_hook_failed", codes.FailedPrecondition)
	// CodeConnectorOrphanedResources is a warning, never returned as an
	// error: a connector was deleted with force after its delete hook failed,
	// so resources it created outside Conduit may remain. It is carried by the
	// DeleteConnector response, logged, and reported by the `conduit doctor`
	// check connectors.orphaned_resources until the orphan is acknowledged
	// (#3058). The gRPC category is unused; FailedPrecondition is what it
	// would mean if it ever were returned.
	CodeConnectorOrphanedResources = conduiterr.Register("connector.orphaned_resources", codes.FailedPrecondition)
	// CodeConnectorOrphanNotFound is raised when an orphan is acknowledged
	// for a connector ID that has no orphan entry.
	CodeConnectorOrphanNotFound = conduiterr.Register("connector.orphan_not_found", codes.NotFound)
)

// deleteHookSuggestion is the Suggestion carried by
// CodeConnectorDeleteHookFailed errors.
const deleteHookSuggestion = "the connector was kept. Its delete hook releases what the connector created outside " +
	"Conduit (for example a Postgres replication slot, which retains WAL until it is dropped). Fix the cause named " +
	"in the error and delete again, or delete with --force (API: force=true) to remove the connector anyway and " +
	"clean those resources up by hand (see docs/operations/connector-delete-hook-failures.md)"

// statePersistSuggestion is the Suggestion carried by
// CodeConnectorStatePersistFailed errors.
const statePersistSuggestion = "nothing was acknowledged upstream for the failed writes; fix the store error " +
	"named in the message and restart the pipeline, which resumes from its last stored position " +
	"(see docs/operations/connector-state-write-failures.md)"
