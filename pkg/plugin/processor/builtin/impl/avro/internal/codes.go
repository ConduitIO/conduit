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

package internal

import (
	"google.golang.org/grpc/codes"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// Error codes of the avro.encode fromRecord schema strategy. Every one of them
// makes the record an error record, which goes to the DLQ or stops the
// pipeline according to its dead-letter settings; nothing is acked upstream
// before that happens.
var (
	// CodeSchemaMismatch is returned by avro.encode with schema.strategy
	// fromRecord when a value does not fit the schema the record carries
	// exactly: a different type, a number out of range, null in a non-null
	// field, a value finer than the logical type (a decimal with more digits
	// than its scale, a time in a date), or a key added or removed inside a
	// nested record. The value is never coerced, truncated or filled with a
	// default. A top-level field the data no longer has is not an error: it is
	// dropped from the registered schema. Fix the processor that changed the
	// value, or attach a schema that describes it.
	CodeSchemaMismatch = conduiterr.Register("processor.avro_encode.schema_mismatch", codes.InvalidArgument)

	// CodeSchemaUnsupported is returned by avro.encode with schema.strategy
	// fromRecord when the record's schema metadata cannot be read (a subject
	// without a version, or a version that is not a positive integer), when
	// the schema it names is not an Avro record, or when the schema cannot be
	// fitted to the record: an added field cannot be added to it, or a removed
	// field defines a named type another field uses.
	CodeSchemaUnsupported = conduiterr.Register("processor.avro_encode.schema_unsupported", codes.FailedPrecondition)

	// CodeRegistryUnavailable is returned by avro.encode with schema.strategy
	// fromRecord when the schema registry could not return the schema the
	// record carries, or could not register the schema used to encode it. The
	// record is not encoded with an inferred schema instead.
	CodeRegistryUnavailable = conduiterr.Register("processor.avro_encode.registry_unavailable", codes.Unavailable)

	// CodeSchemaIncompatible is returned by avro.encode with schema.strategy
	// fromRecord when the schema registry refuses the encoding schema under
	// the compatibility rule of schema.fromRecord.subject, for example because
	// the subject holds earlier autoRegister versions. Use a new subject, or
	// change that subject's compatibility setting deliberately.
	CodeSchemaIncompatible = conduiterr.Register("processor.avro_encode.schema_incompatible", codes.FailedPrecondition)
)
