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

package schemaregistry

import (
	"math"
	"strings"

	"github.com/conduitio/conduit-commons/schema/avro"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// AvroMaxElementsConfigPath is the engine setting that bounds how many
// elements a single Avro array or map may declare when decoded in-process.
const AvroMaxElementsConfigPath = "schema.avro.max-elements"

// DefaultAvroMaxElements is the default for AvroMaxElementsConfigPath. It
// matches conduit-commons' built-in default.
const DefaultAvroMaxElements = 1_000_000

// CodeAvroLimitExceeded is raised when an Avro payload declares an array or
// map with more elements than schema.avro.max-elements allows (default
// 1,000,000). The record is not decoded and nothing is truncated: it fails
// like any other decode error, so it goes to the pipeline's dead-letter
// queue or stops the pipeline according to the DLQ configuration. Raise
// schema.avro.max-elements (0 removes the limit) if such records are
// legitimate. ResourceExhausted: the payload exceeds a configured limit.
var CodeAvroLimitExceeded = conduiterr.Register("schema.avro.limit_exceeded", codes.ResourceExhausted)

// avroLimitMarkers are the decoder messages conduit-commons' Avro codec
// (github.com/iskorotkov/avro/v2) reports when a cap fires. The codec has
// no typed error for this, so the message is the contract;
// TestClassifyDecodeError and the avro.decode regression tests fail if a
// codec upgrade changes it.
var avroLimitMarkers = []string{
	"Config.MaxSliceAllocSize",
	"Config.MaxMapAllocSize",
}

// ClassifyDecodeError returns err tagged with CodeAvroLimitExceeded when it
// is an Avro allocation-cap failure, and err unchanged otherwise (including
// nil). It works on the error text, so it also classifies errors that
// crossed the plugin protocol as strings (destination acks). The message is
// preserved; the code, config path and suggestion are added.
func ClassifyDecodeError(err error) error {
	if err == nil {
		return nil
	}
	if ce, ok := conduiterr.Get(err); ok && ce.Code == CodeAvroLimitExceeded {
		return err
	}
	msg := err.Error()
	for _, m := range avroLimitMarkers {
		if strings.Contains(msg, m) {
			ce := conduiterr.WithCode(err, CodeAvroLimitExceeded)
			ce.ConfigPath = AvroMaxElementsConfigPath
			ce.Suggestion = "the Avro payload declares more array or map elements than " +
				AvroMaxElementsConfigPath + " allows; if such records are legitimate, raise " +
				AvroMaxElementsConfigPath + " (0 removes the limit) and restart Conduit"
			return ce
		}
	}
	return err
}

// ApplyAvroMaxElements sets the process-wide Avro array and map element
// limit used by every Avro decode in this process: the built-in avro.decode
// processor, and the connector-sdk and processor-sdk schema middleware of
// built-in connectors and processors. n == 0 removes the limit. Standalone
// plugins run their own SDK and conduit-commons in their own process and
// are not affected.
//
// It must run before any pipeline starts: conduit-commons caches parsed
// schemas, and a schema parsed earlier keeps the limit it was parsed with
// until its cache entry expires.
func ApplyAvroMaxElements(n int) error {
	if n < 0 {
		return cerrors.Errorf("%s must be >= 0, got %d", AvroMaxElementsConfigPath, n)
	}
	limit := n
	if n == 0 {
		// conduit-commons has no "unlimited" value (it rejects n <= 0);
		// MaxInt is the codec's own effective "no limit".
		limit = math.MaxInt
	}
	if err := avro.SetDefaultMaxSliceAllocSize(limit); err != nil {
		return cerrors.Errorf("set avro array element limit: %w", err)
	}
	if err := avro.SetDefaultMaxMapAllocSize(limit); err != nil {
		return cerrors.Errorf("set avro map element limit: %w", err)
	}
	return nil
}
