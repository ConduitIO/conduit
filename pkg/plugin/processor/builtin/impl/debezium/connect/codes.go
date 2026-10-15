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

package connect

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Stable error codes of the Debezium-compatible output mode that the encoder
// raises. The names are the ones the design fixes (Observability, Errors); they
// are public contracts, so a code is never renamed or reused for a different
// condition.
var (
	// CodeValueOutOfRange: a value cannot be represented in its schema without
	// rounding or truncation (a decimal with more fractional digits than the
	// column scale, an integer wider than its Connect type, NaN or infinity in
	// a precise decimal). Debezium throws in the same cases.
	CodeValueOutOfRange = conduiterr.Register("debezium.value_out_of_range", codes.InvalidArgument)
	// CodeRecordSchemaMismatch: a record value does not match the descriptor
	// it carries (wrong Go type, missing required field, unknown field). This
	// is what a processor placed before debezium.encode causes when it changes
	// a field without keeping it consistent with the descriptor.
	CodeRecordSchemaMismatch = conduiterr.Register("debezium.record_schema_mismatch", codes.InvalidArgument)
	// CodeDescriptorMissing: a record has no descriptor, or the descriptor is
	// not a valid Connect schema.
	CodeDescriptorMissing = conduiterr.Register("debezium.descriptor_missing", codes.InvalidArgument)
	// CodeUnsupportedType: a schema uses a type or logical type the encoder
	// does not implement.
	CodeUnsupportedType = conduiterr.Register("debezium.unsupported_type", codes.Unimplemented)
	// CodeConfigInvalid: an encoder setting has a value outside its allowed
	// set.
	CodeConfigInvalid = conduiterr.Register("debezium.config_invalid", codes.InvalidArgument)
)
