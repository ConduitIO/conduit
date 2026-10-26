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
	// CodeJSScriptTooLarge is raised when script.path (or a required module)
	// names a file larger than 10 MiB. The whole file would be read into
	// memory and compiled, on every pipeline start, by a setting an API writer
	// controls. Nothing is truncated; use a smaller script.
	CodeJSScriptTooLarge = conduiterr.Register("processor.js_script_too_large", codes.ResourceExhausted)
	// CodeJSScriptNotRegular is raised when script.path (or a required module)
	// is not a regular file: a FIFO would block the read forever and a device
	// never ends.
	CodeJSScriptNotRegular = conduiterr.Register("processor.js_script_not_regular", codes.InvalidArgument)
)
