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
	"strings"

	"github.com/conduitio/conduit-commons/schema/avro"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// decimalNotRepresentable turns an avro.ErrDecimalNotRepresentable from
// Serde.Marshal into a coded error that names the field and the precision and
// scale the value needs.
//
// Invariant 6: conduit-commons refuses a decimal that does not fit its schema
// exactly instead of truncating it (it used to write 1.123456789012 against
// decimal(31,10) as 1.1234567890). The record fails here and becomes an error
// record, which goes to the DLQ or stops the pipeline according to its
// dead-letter settings; it is never acked or encoded with a changed value.
//
// With schema.strategy fromRecord the code is CodeSchemaMismatch, which that
// strategy has always returned for a decimal finer than its scale. With
// autoRegister and preRegistered it is CodeDecimalNotRepresentable. hasCarried
// reports whether the record carried a schema; a fromRecord record that
// carries none was encoded with the inferred decimal(31,10), so the suggestion
// points at that inference rather than at a processor.
//
// Unlike other codec errors, the detail is shown: by the contract of
// ErrDecimalNotRepresentable it holds the field path, the precision and scale
// the value needs, the schema's, and a fix, never the value. If its format
// changes, the error keeps its code and only loses the detail.
func decimalNotRepresentable(err error, fromRecord, hasCarried bool) error {
	detail, fix := decimalDetail(err)
	msg := "a decimal value does not fit its Avro schema exactly and is not rounded or truncated to fit"
	if detail != "" {
		msg += ": " + detail
	}

	if fromRecord {
		ce := conduiterr.Wrap(CodeSchemaMismatch, msg, err)
		ce.Suggestion = SuggestionDecimalCarried
		if !hasCarried {
			ce.Suggestion = SuggestionDecimalInferred
		}
		if fix != "" {
			ce.Suggestion += "; for this value: " + fix
		}
		return ce
	}

	ce := conduiterr.Wrap(CodeDecimalNotRepresentable, msg, err)
	ce.Suggestion = SuggestionDecimalStrategy
	if fix != "" {
		ce.Suggestion += "; for this value: " + fix
	}
	return ce
}

// Suggestions of the decimalNotRepresentable error, by case. Each is followed
// by "; for this value: " and the fix conduit-commons computed, when there is
// one.
const (
	// SuggestionDecimalCarried is for fromRecord with a schema the record
	// carries.
	SuggestionDecimalCarried = "the value no longer fits the schema the record carries and is not changed to fit it; " +
		"fix the processor that changed it, or attach a schema that describes it"
	// SuggestionDecimalInferred is for fromRecord with a record that carries
	// no schema, encoded with the inferred decimal(31,10).
	SuggestionDecimalInferred = "the record carries no schema, so avro.encode inferred decimal(31,10), which cannot hold " +
		"the value; attach the source's schema to the record, or use preRegistered with a schema whose decimal has " +
		"enough digits, or round the value before avro.encode"
	// SuggestionDecimalStrategy is for autoRegister and preRegistered.
	SuggestionDecimalStrategy = "use schema.strategy fromRecord to encode with the decimal type the record carries, " +
		"or preRegistered with a schema whose decimal has enough digits, or round the value before avro.encode"
)

// decimalDetail returns the part of an ErrDecimalNotRepresentable message
// after the sentinel, split into the description (`field "d": the value
// needs precision 13 and scale 12, the schema is decimal(31,10)`) and the fix
// after "; fix: ". Both are empty when the message does not have that shape.
func decimalDetail(err error) (detail, fix string) {
	msg := err.Error()
	marker := avro.ErrDecimalNotRepresentable.Error() + ": "
	i := strings.Index(msg, marker)
	if i < 0 {
		return "", ""
	}
	rest := msg[i+len(marker):]
	if !strings.HasPrefix(rest, "field ") {
		return "", ""
	}
	detail, fix, _ = strings.Cut(rest, "; fix: ")
	return detail, fix
}
