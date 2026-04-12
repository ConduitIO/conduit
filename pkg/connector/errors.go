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

package connector

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"google.golang.org/grpc/codes"
)

var (
	// ErrInvalidConnectorType is returned when an invalid connector type is provided.
	ErrInvalidConnectorType = cerrors.NewWithGRPCCode(codes.InvalidArgument, "invalid connector type")
	// ErrInstanceNotFound is returned when a connector with the given ID can't be found.
	ErrInstanceNotFound = cerrors.NewWithGRPCCode(codes.NotFound, "connector instance not found")
	// ErrNameMissing is returned when a connector name is expected but is empty.
	ErrNameMissing = cerrors.NewWithGRPCCode(codes.InvalidArgument, "must provide a connector name")
	// ErrNameOverLimit is returned when a connector name is over the character limit.
	ErrNameOverLimit = cerrors.NewWithGRPCCode(codes.InvalidArgument, "connector name is over the character limit")
	// ErrIDMissing is returned when a connector ID is expected but is empty.
	ErrIDMissing = cerrors.NewWithGRPCCode(codes.InvalidArgument, "must provide a connector ID")
	// ErrInvalidCharacters is returned when a connector ID contains invalid characters.
	ErrInvalidCharacters = cerrors.NewWithGRPCCode(codes.InvalidArgument, "connector ID contains invalid characters")
	// ErrIDOverLimit is returned when a connector ID is over the character limit.
	ErrIDOverLimit = cerrors.NewWithGRPCCode(codes.InvalidArgument, "connector ID is over the character limit")
	// ErrProcessorIDNotFound is returned when a processor with the given ID can't be found in the connector.
	ErrProcessorIDNotFound = cerrors.NewWithGRPCCode(codes.NotFound, "processor ID not found in connector")
	// ErrInvalidConnectorStateType is returned when an invalid connector state type is provided.
	ErrInvalidConnectorStateType = cerrors.NewWithGRPCCode(codes.InvalidArgument, "invalid connector state type")
	// ErrConnectorRunning is returned when an operation can't be executed because the connector is running.
	ErrConnectorRunning = cerrors.NewWithGRPCCode(codes.FailedPrecondition, "connector is running")
	// ErrMissingPlugin is returned when a plugin name is expected but is empty.
	ErrMissingPlugin = cerrors.NewWithGRPCCode(codes.InvalidArgument, "must provide a plugin")
	// ErrMissingPipelineID is returned when a pipeline ID is expected for the connector but is empty.
	ErrMissingPipelineID = cerrors.NewWithGRPCCode(codes.InvalidArgument, "must provide a pipeline ID")
)
