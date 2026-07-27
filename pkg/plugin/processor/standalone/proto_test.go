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

package standalone

import (
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	opencdcv1 "github.com/conduitio/conduit-commons/proto/opencdc/v1"
	sdk "github.com/conduitio/conduit-processor-sdk"
	processorv1 "github.com/conduitio/conduit-processor-sdk/proto/processor/v1"
	"github.com/matryer/is"
)

// TestProtoConverter_ProcessedRecord_MultiRecord is the regression test for a
// gap found while building the RAG pipeline e2e (rag_e2e_test.go): before this
// fix, protoConverter.processedRecord's switch had no case for
// *processorv1.Process_ProcessedRecord_MultiRecord at all, so ANY standalone
// WASM processor returning sdk.MultiRecord from Process (fan-out — one input
// record producing zero, one, or many output records, e.g. a chunking
// processor) would have that response rejected host-side as "unknown
// processed record type: *processorv1.Process_ProcessedRecord_MultiRecord",
// regardless of what the guest itself did correctly. This exercises the fix
// directly against the proto conversion layer, without needing a compiled
// WASM guest.
func TestProtoConverter_ProcessedRecord_MultiRecord(t *testing.T) {
	is := is.New(t)
	c := protoConverter{}

	rec1 := opencdc.Record{Key: opencdc.RawData("doc-1:0"), Payload: opencdc.Change{After: opencdc.RawData("chunk 0")}}
	rec2 := opencdc.Record{Key: opencdc.RawData("doc-1:1"), Payload: opencdc.Change{After: opencdc.RawData("chunk 1")}}

	protoRec1, protoRec2 := &opencdcv1.Record{}, &opencdcv1.Record{}
	is.NoErr(rec1.ToProto(protoRec1))
	is.NoErr(rec2.ToProto(protoRec2))

	in := &processorv1.Process_ProcessedRecord{
		Record: &processorv1.Process_ProcessedRecord_MultiRecord{
			MultiRecord: &processorv1.Process_MultiRecord{
				Records: []*opencdcv1.Record{protoRec1, protoRec2},
			},
		},
	}

	out, err := c.processedRecord(in)
	is.NoErr(err)

	multi, ok := out.(sdk.MultiRecord)
	if !ok {
		t.Fatalf("want sdk.MultiRecord, got %T (%+v)", out, out)
	}
	is.Equal(len(multi), 2)
	is.Equal(multi[0].Key, opencdc.RawData("doc-1:0"))
	is.Equal(multi[1].Key, opencdc.RawData("doc-1:1"))
}

// TestProtoConverter_ProcessedRecord_MultiRecord_Empty proves the empty
// fan-out case (a processor's documented "filter" equivalent — see
// sdk.MultiRecord's own doc comment) converts to a real, non-nil, zero-length
// sdk.MultiRecord — not a nil slice and not an error — mirroring
// singleRecord's own nil-guard behavior for a nil inner message.
func TestProtoConverter_ProcessedRecord_MultiRecord_Empty(t *testing.T) {
	is := is.New(t)
	c := protoConverter{}

	in := &processorv1.Process_ProcessedRecord{
		Record: &processorv1.Process_ProcessedRecord_MultiRecord{
			MultiRecord: &processorv1.Process_MultiRecord{},
		},
	}

	out, err := c.processedRecord(in)
	is.NoErr(err)

	multi, ok := out.(sdk.MultiRecord)
	if !ok {
		t.Fatalf("want sdk.MultiRecord, got %T (%+v)", out, out)
	}
	is.True(multi != nil)
	is.Equal(len(multi), 0)
}
