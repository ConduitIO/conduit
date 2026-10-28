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
	"math"

	"github.com/conduitio/conduit-commons/opencdc"
	"google.golang.org/protobuf/encoding/protowire"
)

// writeLimitMarginPercent is the share of connectors.max-receive-record-size a
// multi-record Write may use. recordWireSize is exact, so this only leaves room
// for request framing the estimate does not model.
const writeLimitMarginPercent = 99

// recordWireSize returns the bytes a record adds to a DestinationRunRequest (its
// `records` field tag, length and body), which is what a standalone connector's
// gRPC server bounds with MaxRecvMsgSize (connectors.max-receive-record-size).
//
// It mirrors the protobuf encoding of opencdc.v1.Record, taking the larger of
// what the v2 and v3 encoders write wherever they differ (integers inside
// structured data), using protowire size functions, and never allocates the
// encoded output. It is deliberately not
// the JSON size used for metrics (metrics.RecordBytesHistogram.SizeOf): structured
// data travels as a google.protobuf.Struct, where a number costs a 9-byte Value
// plus tags against 1-2 bytes of JSON, and every list element and map entry
// carries its own tag and varint length. TestRecordWireSize_MatchesProtocolEncoders
// pins it against the real v2 and v3 encoders.
//
// Where it cannot be exact (a value type the encoders do not support) it errs
// high: an over-estimate only splits a Write earlier, an under-estimate fails the
// whole batch at the plugin.
func recordWireSize(r opencdc.Record) int {
	body := 0
	if n := len(r.Position); n > 0 {
		body += protowire.SizeTag(1) + protowire.SizeBytes(n)
	}
	if r.Operation != 0 {
		body += protowire.SizeTag(2) + protowire.SizeVarint(uint64(max(int(r.Operation), 0)))
	}
	for k, v := range r.Metadata {
		// map<string,string> entry: a message with key (1) and value (2). The Go
		// encoder writes both fields of an entry even when empty.
		entry := protowire.SizeTag(1) + protowire.SizeBytes(len(k)) +
			protowire.SizeTag(2) + protowire.SizeBytes(len(v))
		body += protowire.SizeTag(3) + protowire.SizeBytes(entry)
	}
	if r.Key != nil {
		body += protowire.SizeTag(4) + protowire.SizeBytes(dataWireSize(r.Key))
	}
	// The encoders always emit the payload message, empty or not.
	change := 0
	if r.Payload.Before != nil {
		change += protowire.SizeTag(1) + protowire.SizeBytes(dataWireSize(r.Payload.Before))
	}
	if r.Payload.After != nil {
		change += protowire.SizeTag(2) + protowire.SizeBytes(dataWireSize(r.Payload.After))
	}
	body += protowire.SizeTag(5) + protowire.SizeBytes(change)
	return protowire.SizeTag(1) + protowire.SizeBytes(body)
}

// dataWireSize is the encoded size of an opencdc.v1.Data message.
func dataWireSize(d opencdc.Data) int {
	switch v := d.(type) {
	case opencdc.RawData:
		return protowire.SizeTag(1) + protowire.SizeBytes(len(v))
	case opencdc.StructuredData:
		return protowire.SizeTag(2) + protowire.SizeBytes(structWireSize(map[string]any(v)))
	default:
		return protowire.SizeTag(1) + protowire.SizeBytes(len(d.Bytes()))
	}
}

// structWireSize is the encoded size of a google.protobuf.Struct.
func structWireSize(m map[string]any) int {
	n := 0
	for k, v := range m {
		entry := protowire.SizeTag(1) + protowire.SizeBytes(len(k)) +
			protowire.SizeTag(2) + protowire.SizeBytes(valueWireSize(v))
		n += protowire.SizeTag(1) + protowire.SizeBytes(entry)
	}
	return n
}

// valueWireSize is the encoded size of a google.protobuf.Value.
func valueWireSize(v any) int {
	switch val := v.(type) {
	case nil:
		return protowire.SizeTag(1) + protowire.SizeVarint(0) // null_value
	case bool:
		return protowire.SizeTag(4) + protowire.SizeVarint(1) // bool_value
	case string:
		return protowire.SizeTag(3) + protowire.SizeBytes(len(val))
	case []byte:
		// Encoded as a base64 string.
		return protowire.SizeTag(3) + protowire.SizeBytes(base64.StdEncoding.EncodedLen(len(val)))
	case map[string]any:
		return protowire.SizeTag(5) + protowire.SizeBytes(structWireSize(val))
	case opencdc.StructuredData:
		return protowire.SizeTag(5) + protowire.SizeBytes(structWireSize(map[string]any(val)))
	case []any:
		list := 0
		for _, e := range val {
			list += protowire.SizeTag(1) + protowire.SizeBytes(valueWireSize(e))
		}
		return protowire.SizeTag(6) + protowire.SizeBytes(list)
	case float64, float32:
		return protowire.SizeTag(2) + 8 // number_value (double)
	case int:
		return intWireSize(int64(val))
	case int8:
		return intWireSize(int64(val))
	case int16:
		return intWireSize(int64(val))
	case int32:
		return intWireSize(int64(val))
	case int64:
		return intWireSize(val)
	case uint:
		return uintWireSize(uint64(val))
	case uint8:
		return uintWireSize(uint64(val))
	case uint16:
		return uintWireSize(uint64(val))
	case uint32:
		return uintWireSize(uint64(val))
	case uint64:
		return uintWireSize(val)
	default:
		// Not a type the encoders accept; size it as its printed form, which errs high.
		return protowire.SizeTag(3) + protowire.SizeBytes(len(fmt.Sprint(v)))
	}
}

// writeLimit converts connectors.max-receive-record-size into the byte budget
// of one multi-record Write, or 0 for "no limit".
func writeLimit(maxReceiveRecordSize int) int {
	if maxReceiveRecordSize <= 0 {
		return 0
	}
	return maxReceiveRecordSize * writeLimitMarginPercent / 100
}

// nextChunk returns the end (exclusive) of the chunk starting at from, and
// whether that chunk is a single record too large to ever be received.
//
// A chunk is the longest run of records whose total wire size stays within
// limit. A record that alone exceeds limit still forms a chunk of one (it cannot
// be split). If it also exceeds maxReceive, the connector's receive limit, the
// request can never succeed: it would fail the whole stream at the plugin with
// RESOURCE_EXHAUSTED and, replayed on restart, stall the pipeline on that one
// record. The caller nacks it instead of sending it (oversize == true). limit
// and maxReceive of 0 mean no limit.
func nextChunk(records []opencdc.Record, from, limit, maxReceive int) (end int, oversize bool) {
	if limit <= 0 && maxReceive <= 0 {
		return len(records), false
	}
	first := recordWireSize(records[from])
	if maxReceive > 0 && first > maxReceive {
		return from + 1, true
	}
	size := first
	end = from + 1
	for end < len(records) {
		s := recordWireSize(records[end])
		if (maxReceive > 0 && s > maxReceive) || (limit > 0 && size+s > limit) {
			break
		}
		size += s
		end++
	}
	return end, false
}

// intWireSize and uintWireSize size an integer inside a google.protobuf.Value.
// The v2 encoder writes it as number_value, a double (9 bytes with its tag); the
// v3 encoder writes it as a varint, which is 11 bytes for a negative number. The
// plugin may speak either, so the larger of the two is used.
func intWireSize(v int64) int {
	if v < 0 {
		// A negative int64 is a 10-byte varint (two's complement).
		return protowire.SizeTag(2) + protowire.SizeVarint(math.MaxUint64)
	}
	return uintWireSize(uint64(v))
}

func uintWireSize(v uint64) int {
	return max(protowire.SizeTag(2)+8, protowire.SizeTag(2)+protowire.SizeVarint(v))
}
