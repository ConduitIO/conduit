// Copyright © 2022 Meroxa, Inc.
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

package status

import (
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/orchestrator"
	"github.com/conduitio/conduit/pkg/pipeline"
	conn_plugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/processor"
	"google.golang.org/grpc/codes"
)

// conduitErrorStatus returns the structured gRPC status for a ConduitError in err's
// chain — its gRPC category plus a google.rpc.ErrorInfo detail carrying the stable
// reason and any configPath/suggestion/fix — and true if one was found. This is the
// single point where the ConduitError model reaches the API; boundaries that have
// not yet been migrated fall through to the sentinel-based code mapping below,
// unchanged.
func conduitErrorStatus(err error) (error, bool) {
	if ce, ok := conduiterr.Get(err); ok {
		return conduiterr.ToStatus(ce).Err(), true
	}
	return nil, false
}

// fallbackStatus is the sink for every one of this package's four boundary
// functions once conduitErrorStatus finds no *ConduitError in err's chain. It
// guarantees the wire is never codeless: category is whatever the caller's
// legacy cerrors.Is matching already determined (preserved unchanged, so this
// is purely additive), and reason is conduiterr.CodeUnknown's
// ("internal.unknown") until the corresponding sentinel is migrated to a
// registered Code at its origination site (see conduiterr.WithUnknownReason).
//
// Before this existed, this path returned a bare grpcstatus.Error with no
// google.rpc.ErrorInfo detail at all — a codeless status that only an
// end-to-end production trip through the API would reveal, not a unit test.
// The codes_contract_test.go guard (execution plan §1.1) now fails the build
// instead.
func fallbackStatus(category codes.Code, err error) error {
	return conduiterr.ToStatus(conduiterr.WithUnknownReason(err, category)).Err()
}

func PipelineError(err error) error {
	if s, ok := conduitErrorStatus(err); ok {
		return s
	}

	var code codes.Code

	switch {
	case cerrors.Is(err, pipeline.ErrNameMissing):
		code = codes.InvalidArgument
	case cerrors.Is(err, pipeline.ErrInstanceNotFound):
		code = codes.NotFound
	default:
		code = codeFromError(err)
	}

	return fallbackStatus(code, err)
}

func ConnectorError(err error) error {
	if s, ok := conduitErrorStatus(err); ok {
		return s
	}

	var code codes.Code

	switch {
	case cerrors.Is(err, connector.ErrInvalidConnectorType):
		code = codes.InvalidArgument
	case cerrors.Is(err, connector.ErrInstanceNotFound):
		code = codes.NotFound
	default:
		code = codeFromError(err)
	}

	return fallbackStatus(code, err)
}

func ProcessorError(err error) error {
	if s, ok := conduitErrorStatus(err); ok {
		return s
	}

	var code codes.Code

	switch {
	case cerrors.Is(err, orchestrator.ErrInvalidProcessorParentType):
		code = codes.InvalidArgument
	case cerrors.Is(err, processor.ErrInstanceNotFound):
		code = codes.NotFound
	default:
		code = codeFromError(err)
	}

	return fallbackStatus(code, err)
}

func PluginError(err error) error {
	if s, ok := conduitErrorStatus(err); ok {
		return s
	}
	return fallbackStatus(codeFromError(err), err)
}

func codeFromError(err error) codes.Code {
	switch {
	case cerrors.Is(err, cerrors.ErrNotImpl):
		return codes.Unimplemented
	case cerrors.Is(err, cerrors.ErrEmptyID):
		return codes.InvalidArgument
	case cerrors.Is(err, pipeline.ErrPipelineRunning):
		return codes.FailedPrecondition
	case cerrors.Is(err, pipeline.ErrPipelineNotRunning):
		return codes.FailedPrecondition
	case cerrors.Is(err, pipeline.ErrNameAlreadyExists):
		return codes.AlreadyExists
	case cerrors.Is(err, connector.ErrConnectorRunning):
		return codes.FailedPrecondition
	case cerrors.Is(err, &conn_plugin.ValidationError{}):
		return codes.FailedPrecondition
	case cerrors.Is(err, orchestrator.ErrPipelineHasConnectorsAttached):
		return codes.FailedPrecondition
	case cerrors.Is(err, orchestrator.ErrPipelineHasProcessorsAttached):
		return codes.FailedPrecondition
	case cerrors.Is(err, orchestrator.ErrConnectorHasProcessorsAttached):
		return codes.FailedPrecondition
	case cerrors.Is(err, orchestrator.ErrImmutableProvisionedByConfig):
		return codes.FailedPrecondition
	default:
		return codes.Internal
	}
}
