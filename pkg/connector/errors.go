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

import "github.com/conduitio/conduit/pkg/foundation/cerrors"

var (
	ErrInstanceNotFound          = cerrors.New("connector instance not found")
	ErrInvalidConnectorType      = cerrors.New("invalid connector type")
	ErrInvalidConnectorStateType = cerrors.New("invalid connector state type")
	ErrProcessorIDNotFound       = cerrors.New("processor ID not found")
	ErrConnectorRunning          = cerrors.New("connector is running")
	ErrInvalidCharacters         = cerrors.New("connector ID contains invalid characters")
	ErrIDOverLimit               = cerrors.New("connector ID is over the character limit (64)")
	ErrNameOverLimit             = cerrors.New("connector name is over the character limit (64)")
	ErrNameMissing               = cerrors.New("must provide a connector name")
	ErrIDMissing                 = cerrors.New("must provide a connector ID")
)
