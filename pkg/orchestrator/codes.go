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

package orchestrator

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
	"google.golang.org/grpc/codes"
)

// Orchestrator error codes. Every error carries one of these codes plus a
// suggested fix, so an API, MCP, or UI consumer knows what happened without
// parsing message text.
var (
	// CodeInvalidProcessorParentType is raised when a processor's parent is
	// neither a pipeline nor a connector.
	CodeInvalidProcessorParentType = conduiterr.Register("orchestrator.invalid_processor_parent_type", codes.InvalidArgument)
	// CodePipelineHasProcessorsAttached is raised when a pipeline cannot be
	// deleted because it still has processors attached.
	CodePipelineHasProcessorsAttached = conduiterr.Register("orchestrator.pipeline_has_processors_attached", codes.FailedPrecondition)
	// CodePipelineHasConnectorsAttached is raised when a pipeline cannot be
	// deleted because it still has connectors attached.
	CodePipelineHasConnectorsAttached = conduiterr.Register("orchestrator.pipeline_has_connectors_attached", codes.FailedPrecondition)
	// CodeConnectorHasProcessorsAttached is raised when a connector cannot
	// be deleted because it still has processors attached.
	CodeConnectorHasProcessorsAttached = conduiterr.Register("orchestrator.connector_has_processors_attached", codes.FailedPrecondition)
	// CodeImmutableProvisionedByConfig is raised when a pipeline, connector,
	// or processor provisioned by a config file is mutated through the API
	// instead of the config file.
	CodeImmutableProvisionedByConfig = conduiterr.Register("orchestrator.immutable_provisioned_by_config", codes.FailedPrecondition)
)

// pipelineRunningErr wraps pipeline.ErrPipelineRunning with the
// pipeline.CodePipelineRunning code and a suggestion. msg is the
// boundary-level, human-readable message (Wrap replaces, not concatenates,
// the wrapped cause's text).
//
// Invariant: errors.Is(err, pipeline.ErrPipelineRunning) still holds — the
// sentinel is wrapped, and the ConduitError adds the machine-actionable code.
func pipelineRunningErr(msg string) error {
	e := conduiterr.Wrap(pipeline.CodePipelineRunning, msg, pipeline.ErrPipelineRunning)
	e.Suggestion = "the pipeline is running; stop it before making this change"
	return e
}

// immutableProvisionedByConfigErr wraps ErrImmutableProvisionedByConfig with
// the CodeImmutableProvisionedByConfig code and a suggestion. msg is the
// boundary-level, human-readable message.
//
// Invariant: errors.Is(err, ErrImmutableProvisionedByConfig) still holds —
// the sentinel is wrapped, and the ConduitError adds the machine-actionable
// code.
func immutableProvisionedByConfigErr(msg string) error {
	e := conduiterr.Wrap(CodeImmutableProvisionedByConfig, msg, ErrImmutableProvisionedByConfig)
	e.Suggestion = "change the corresponding pipeline configuration file instead"
	return e
}

// invalidProcessorParentTypeErr wraps ErrInvalidProcessorParentType with the
// CodeInvalidProcessorParentType code and a suggestion. msg is the
// boundary-level, human-readable message.
//
// Invariant: errors.Is(err, ErrInvalidProcessorParentType) still holds — the
// sentinel is wrapped, and the ConduitError adds the machine-actionable
// code.
func invalidProcessorParentTypeErr(msg string) error {
	e := conduiterr.Wrap(CodeInvalidProcessorParentType, msg, ErrInvalidProcessorParentType)
	e.Suggestion = `set the processor parent type to either "pipeline" or "connector"`
	return e
}
