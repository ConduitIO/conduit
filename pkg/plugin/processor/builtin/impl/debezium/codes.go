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

package debezium

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// Stable error codes of the debezium.encode processor. Each names a failing
// config path or record location in its Suggestion, never a value (CLAUDE.md:
// errors are API).
var (
	// CodeConfigInvalid is raised when the processor settings are invalid.
	CodeConfigInvalid = conduiterr.Register("debezium.config_invalid", codes.InvalidArgument)
	// CodeDescriptorMissing is raised when a record carries no
	// debezium.descriptor, or a descriptor that does not match this
	// format.version.
	CodeDescriptorMissing = conduiterr.Register("debezium.descriptor_missing", codes.FailedPrecondition)
	// CodeRecordSchemaMismatch is raised when a record's key, before or after
	// does not match the descriptor: a missing required field, an unknown
	// field, or a value of the wrong type. It never coerces (invariant 6).
	CodeRecordSchemaMismatch = conduiterr.Register("debezium.record_schema_mismatch", codes.InvalidArgument)
	// CodeValueOutOfRange is raised when a value does not fit its declared
	// schema (a decimal wider than its scale, a timestamp outside the epoch
	// microsecond range).
	CodeValueOutOfRange = conduiterr.Register("debezium.value_out_of_range", codes.InvalidArgument)
	// CodeUnsupportedType is raised when a descriptor names a Connect type
	// this encoder cannot serialize.
	CodeUnsupportedType = conduiterr.Register("debezium.unsupported_type", codes.InvalidArgument)
	// CodeOldKeyUnavailable is raised on a primary-key change whose
	// debezium.old_key is missing or unparsable, so the delete and tombstone
	// cannot be keyed.
	CodeOldKeyUnavailable = conduiterr.Register("debezium.old_key_unavailable", codes.InvalidArgument)
	// CodeAvroUnsupported is raised when key.converter or value.converter is
	// "avro": the Confluent AvroConverter (AvroData port, subject naming,
	// Confluent wire format) is not implemented yet. It fails at configure
	// time, before any record is encoded, so a misconfigured pipeline never
	// emits a half-encoded message.
	CodeAvroUnsupported = conduiterr.Register("debezium.avro_unsupported", codes.Unimplemented)
)
