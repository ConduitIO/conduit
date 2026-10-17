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

package protobuf

import (
	"google.golang.org/grpc/codes"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

var (
	// CodeSchemaCompile is returned when the .proto source fails to compile.
	CodeSchemaCompile = conduiterr.Register("processor.protobuf_decode.schema_compile", codes.InvalidArgument)

	// CodeReferenceResolve is returned when a schema reference cannot be
	// resolved, or the reference graph forms a cycle.
	CodeReferenceResolve = conduiterr.Register("processor.protobuf_decode.reference_resolve", codes.FailedPrecondition)

	// CodeMessageIndex is returned when the Confluent message-index prefix is
	// malformed or out of range.
	CodeMessageIndex = conduiterr.Register("processor.protobuf_decode.message_index", codes.InvalidArgument)

	// CodeUnknownField is returned when the payload carries a field the schema
	// does not declare and the policy is reject.
	CodeUnknownField = conduiterr.Register("processor.protobuf_decode.unknown_field", codes.InvalidArgument)

	// CodeWireFormat is returned when the payload is not valid protobuf wire
	// format for the schema.
	CodeWireFormat = conduiterr.Register("processor.protobuf_decode.wire_format", codes.InvalidArgument)
)
