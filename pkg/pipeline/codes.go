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
)
