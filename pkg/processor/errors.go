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

package processor

import "github.com/conduitio/conduit/pkg/foundation/cerrors"

var (
	// ErrSkipRecord is passed by a processor when it should Ack and skip a Record.
	// It must be separate from a plain error so that we continue instead of marking
	// the Pipeline status as degraded.
	ErrSkipRecord       = cerrors.New("record skipped")
	ErrInstanceNotFound = cerrors.New("processor instance not found")
)
