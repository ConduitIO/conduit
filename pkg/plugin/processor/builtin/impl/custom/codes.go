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

package custom

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Stable error codes of the custom.javascript processor.
var (
	// CodeJSCyclicOutput is raised when the record a script returns contains
	// a map or slice that holds itself, directly or through other values (for
	// example `record.Payload.After["self"] = record.Payload.After`). Such a
	// record cannot be cloned, encoded or walked: every recursive consumer
	// would overflow the stack, which is a fatal runtime error that takes
	// down every pipeline in the process. The record is not emitted. It
	// fails like any other processor error: with a DLQ enabled it is routed
	// to the dead-letter queue, otherwise the pipeline degrades. The message
	// names the path where the cycle closes.
	CodeJSCyclicOutput = conduiterr.Register("processor.js_cyclic_output", codes.InvalidArgument)
	// CodeJSOutputLimitExceeded is raised when the record a script returns
	// nests deeper than 10,000 levels, or expands to more than about 16.7
	// million map entries and slice elements once shared values are counted
	// at each place they appear (a value referenced from many parents is
	// copied once per parent by Record.Clone). Both would exhaust the stack
	// or memory of the whole process in the next recursive consumer. The
	// record is not emitted and nothing is truncated; it fails like any
	// other processor error. ResourceExhausted: the output exceeds a fixed
	// limit.
	CodeJSOutputLimitExceeded = conduiterr.Register("processor.js_output_limit_exceeded", codes.ResourceExhausted)
)
