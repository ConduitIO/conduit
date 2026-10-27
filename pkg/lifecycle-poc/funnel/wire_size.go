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

package funnel

import (
	"encoding/base64"
	"fmt"

	"github.com/conduitio/conduit-commons/opencdc"
)

// writeLimitMarginPercent is the share of connectors.max-receive-record-size a
// single Write may use. The wire estimate below is already an upper-biased
// walk, the margin covers the request framing around the records.
const writeLimitMarginPercent = 95

// recordWireSize estimates the bytes a record occupies in a
// DestinationRunRequest, which is what a standalone connector's gRPC server
// bounds with MaxRecvMsgSize (connectors.max-receive-record-size).
//
// It is deliberately not the JSON size used for metrics
// (metrics.RecordBytesHistogram.SizeOf): structured data travels as a
// google.protobuf.Struct, where a number costs a 9-byte Value plus tags (about
// 11 bytes against 1-2 bytes of JSON) and every map entry carries its own
// framing. It never allocates the encoded output. It errs high, since an
// over-estimate only splits a Write a little earlier, while an under-estimate
// fails the whole batch at the plugin.
func recordWireSize(r opencdc.Record) int {
	n := 24 + len(r.Position) + 4 // operation, tags, position
	for k, v := range r.Metadata {
		n += len(k) + len(v) + 8
	}
	n += dataWireSize(r.Key)
	n += dataWireSize(r.Payload.Before)
	n += dataWireSize(r.Payload.After)
	return n
}

func dataWireSize(d opencdc.Data) int {
	switch v := d.(type) {
	case nil:
		return 0
	case opencdc.RawData:
		return len(v) + 4
	case opencdc.StructuredData:
		return structWireSize(map[string]any(v)) + 4
	default:
		return len(d.Bytes()) + 4
	}
}

func structWireSize(m map[string]any) int {
	n := 4
	for k, v := range m {
		n += len(k) + 8 + valueWireSize(v)
	}
	return n
}

func valueWireSize(v any) int {
	switch val := v.(type) {
	case nil:
		return 4
	case bool:
		return 4
	case string:
		return len(val) + 4
	case []byte:
		return base64.StdEncoding.EncodedLen(len(val)) + 4
	case map[string]any:
		return structWireSize(val)
	case opencdc.StructuredData:
		return structWireSize(map[string]any(val))
	case []any:
		n := 4
		for _, e := range val {
			n += valueWireSize(e)
		}
		return n
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return 11
	default:
		return len(fmt.Sprint(v)) + 4
	}
}

// writeLimit converts connectors.max-receive-record-size into the byte budget
// of one Write, or 0 for "no limit".
func writeLimit(maxReceiveRecordSize int) int {
	if maxReceiveRecordSize <= 0 {
		return 0
	}
	return maxReceiveRecordSize * writeLimitMarginPercent / 100
}

// chunkEnd returns the end (exclusive) of the longest run of records starting
// at from whose estimated wire size stays within limit. A record that alone
// exceeds limit still forms a chunk of one: it cannot be split, and the plugin
// reports the oversize error for exactly that record. limit <= 0 means no limit.
func chunkEnd(records []opencdc.Record, from, limit int) int {
	if limit <= 0 {
		return len(records)
	}
	size := 0
	end := from
	for end < len(records) {
		s := recordWireSize(records[end])
		if end > from && size+s > limit {
			break
		}
		size += s
		end++
	}
	return end
}
