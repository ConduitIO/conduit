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

package pipeline

import "github.com/conduitio/conduit/pkg/foundation/cerrors"

var (
	ErrGracefulShutdown      = cerrors.New("graceful shutdown")
	ErrForceStop             = cerrors.New("force stop")
	ErrPipelineCannotRecover = cerrors.New("pipeline couldn't be recovered")
	ErrPipelineRunning       = cerrors.New("pipeline is running")
	ErrPipelineNotRunning    = cerrors.New("pipeline not running")
	ErrInstanceNotFound      = cerrors.New("pipeline instance not found")
	ErrNameMissing           = cerrors.New("must provide a pipeline name")
	ErrIDMissing             = cerrors.New("must provide a pipeline ID")
	ErrNameAlreadyExists     = cerrors.New("pipeline name already exists")
	ErrInvalidCharacters     = cerrors.New("pipeline ID contains invalid characters")
	ErrNameOverLimit         = cerrors.New("pipeline name is over the character limit (64)")
	ErrIDOverLimit           = cerrors.New("pipeline ID is over the character limit (64)")
	ErrDescriptionOverLimit  = cerrors.New("pipeline description is over the character limit (8192)")
	ErrConnectorIDNotFound   = cerrors.New("connector ID not found")
	ErrProcessorIDNotFound   = cerrors.New("processor ID not found")
)
