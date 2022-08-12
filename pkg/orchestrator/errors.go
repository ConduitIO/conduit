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

package orchestrator

import "github.com/conduitio/conduit/pkg/foundation/cerrors"

var (
	ErrInvalidProcessorParentType     = cerrors.New("invalid processor parent type")
	ErrPipelineHasProcessorsAttached  = cerrors.New("pipeline has processors attached")
	ErrPipelineHasConnectorsAttached  = cerrors.New("pipeline has connectors attached")
	ErrConnectorHasProcessorsAttached = cerrors.New("connector has processors attached")
	ErrImmutableProvisionedByConfig   = cerrors.New("entity was provisioned by a config file and cannot be mutated through the API, please change the corresponding config file instead")
)
