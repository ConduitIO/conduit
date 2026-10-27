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
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	v2 "github.com/conduitio/conduit-connector-protocol/pconnector/v2/toproto"
	v3 "github.com/conduitio/conduit-connector-protocol/pconnector/v3/toproto"
	"github.com/matryer/is"
	"google.golang.org/protobuf/proto"
)

// encodedSizes returns the size of the DestinationRunRequest the real v2 and v3
// encoders produce for recs, i.e. what the plugin's gRPC server measures against
// MaxRecvMsgSize.
func encodedSizes(t *testing.T, recs []opencdc.Record) (v2Size, v3Size int) {
	t.Helper()
	req := pconnector.DestinationRunRequest{Records: recs}
	p2, err := v2.DestinationRunRequest(req)
	if err != nil {
		t.Fatalf("v2 encode: %v", err)
	}
	b, err := v3.DestinationRunRequestBytes(nil, req)
	if err != nil {
		t.Fatalf("v3 encode: %v", err)
	}
	return proto.Size(p2), len(b)
}

func estimateAll(recs []opencdc.Record) int {
	n := 0
	for _, r := range recs {
		n += recordWireSize(r)
	}
	return n
}

func wireRec(key, before, after opencdc.Data, pos int, md opencdc.Metadata) opencdc.Record {
	return opencdc.Record{
		Position:  bytes.Repeat([]byte("p"), pos),
		Operation: opencdc.OperationCreate,
		Metadata:  md,
		Key:       key,
		Payload:   opencdc.Change{Before: before, After: after},
	}
}

// TestRecordWireSize_MatchesProtocolEncoders is the golden test for the byte cap:
// the estimate must equal the size of the request the larger of the real v2 and v3
// encoders produces, for the shapes that break an approximation (elements of 128 bytes or
// more, which carry 2-byte length varints, and deep nesting), and must never be
// lower for random nested data. An estimate that is too low lets a chunk through
// that the plugin then rejects.
func TestRecordWireSize_MatchesProtocolEncoders(t *testing.T) {
	big := strings.Repeat("x", 300<<10)
	s128 := strings.Repeat("y", 128)

	nestedList := func(depth, n int) any {
		var v any = s128
		for i := 0; i < depth; i++ {
			v = []any{v}
		}
		l := make([]any, n)
		for i := range l {
			l[i] = v
		}
		return l
	}
	nestedMap := map[string]any{"leaf": 1}
	for i := 0; i < 50; i++ {
		nestedMap = map[string]any{"n": nestedMap}
	}
	md := opencdc.Metadata{}
	for i := 0; i < 500; i++ {
		md[fmt.Sprint(i)] = ""
	}
	ints := opencdc.StructuredData{}
	for i := 0; i < 2000; i++ {
		ints[fmt.Sprint(i)] = i - 1000
	}
	emptyLists := make([]any, 5000)
	for i := range emptyLists {
		emptyLists[i] = []any{}
	}

	negInts := opencdc.StructuredData{}
	for i := 0; i < 500; i++ {
		negInts[fmt.Sprint(i)] = -i - 1
	}

	cases := map[string][]opencdc.Record{
		"negative ints":    {wireRec(nil, nil, negInts, 1, nil)},
		"empty record":     {{}},
		"raw":              {wireRec(opencdc.RawData("k"), nil, opencdc.RawData("0123456789"), 1, nil)},
		"raw big":          {wireRec(nil, nil, opencdc.RawData(big), 1, nil)},
		"key before after": {wireRec(opencdc.RawData(big[:1000]), opencdc.RawData(big[:1000]), opencdc.RawData(big[:1000]), 1, nil)},
		"large position":   {wireRec(nil, nil, nil, 70000, nil)},
		"metadata tiny":    {wireRec(nil, nil, nil, 1, md)},
		"metadata big":     {wireRec(nil, nil, nil, 1, opencdc.Metadata{"k": big})},
		"struct ints":      {wireRec(nil, nil, ints, 1, nil)},
		"struct string":    {wireRec(nil, nil, opencdc.StructuredData{"s": big}, 1, nil)},
		"struct bytes":     {wireRec(nil, nil, opencdc.StructuredData{"b": []byte(big)}, 1, nil)},
		"struct bools":     {wireRec(nil, nil, opencdc.StructuredData{"t": true, "f": false, "n": nil}, 1, nil)},
		"nested map":       {wireRec(nil, nil, opencdc.StructuredData(nestedMap), 1, nil)},
		"empty lists":      {wireRec(nil, nil, opencdc.StructuredData{"l": emptyLists}, 1, nil)},
		"list 128B x 2k":   {wireRec(nil, nil, opencdc.StructuredData{"l": nestedList(0, 2000)}, 1, nil)},
		"list depth 1":     {wireRec(nil, nil, opencdc.StructuredData{"l": nestedList(1, 2000)}, 1, nil)},
		"list depth 5":     {wireRec(nil, nil, opencdc.StructuredData{"l": nestedList(5, 2000)}, 1, nil)},
		"list depth 12":    {wireRec(nil, nil, opencdc.StructuredData{"l": nestedList(12, 2000)}, 1, nil)},
		"many records":     {wireRec(opencdc.RawData("k"), nil, opencdc.StructuredData{"a": 1}, 8, opencdc.Metadata{"opencdc.readAt": "1700000000000000000"}), wireRec(nil, nil, nil, 0, nil)},
	}
	for name, recs := range cases {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			s2, s3 := encodedSizes(t, recs)
			got := estimateAll(recs)
			is.True(got >= max(s2, s3)) // never below either encoder
			if name != "struct ints" {
				// Exact against the larger encoder. Mixed-sign integers are the
				// one shape where taking the larger encoding per value can exceed
				// the larger total: v2 writes every number as a double, v3 writes
				// negatives as 10-byte varints.
				is.Equal(got, max(s2, s3))
			}
		})
	}
}

// TestRecordWireSize_NeverBelowEncodersOnRandomData: random nested records, the
// estimate is never below the larger real encoding.
func TestRecordWireSize_NeverBelowEncodersOnRandomData(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	var gen func(d int) any
	gen = func(d int) any {
		switch k := r.Intn(9); {
		case k == 0 || d > 4:
			return r.Intn(1000) - 500
		case k == 1:
			return strings.Repeat("z", r.Intn(300))
		case k == 2:
			return r.Intn(2) == 0
		case k == 3:
			return nil
		case k == 4:
			return r.Float64()
		case k == 5:
			return []byte(strings.Repeat("b", r.Intn(300)))
		case k == 6:
			m := map[string]any{}
			for i := 0; i < r.Intn(5); i++ {
				m[strings.Repeat("k", r.Intn(200))+fmt.Sprint(i)] = gen(d + 1)
			}
			return m
		default:
			lst := make([]any, r.Intn(5))
			for i := range lst {
				lst[i] = gen(d + 1)
			}
			return lst
		}
	}
	for i := 0; i < 1500; i++ {
		m := opencdc.StructuredData{}
		for j := 0; j < 1+r.Intn(20); j++ {
			m[fmt.Sprint(j)] = gen(0)
		}
		recs := []opencdc.Record{wireRec(nil, nil, m, r.Intn(300), opencdc.Metadata{"a": strings.Repeat("m", r.Intn(300))})}
		s2, s3 := encodedSizes(t, recs)
		if got := estimateAll(recs); got < s2 || got < s3 {
			t.Fatalf("random #%d: estimate %d below encoded size v2=%d v3=%d", i, got, s2, s3)
		}
	}
}

// TestNextChunk_StaysWithinRealLimit: chunks cut by nextChunk, encoded by the
// real encoders, never exceed connectors.max-receive-record-size. This is the
// end-to-end property the byte cap exists for, on the deep-list shape that an
// approximate estimator under-counts.
func TestNextChunk_StaysWithinRealLimit(t *testing.T) {
	is := is.New(t)
	const maxReceive = 4 << 20
	var v any = strings.Repeat("y", 128)
	for i := 0; i < 8; i++ {
		v = []any{v}
	}
	recs := make([]opencdc.Record, 200)
	for i := range recs {
		recs[i] = wireRec(nil, nil, opencdc.StructuredData{"l": []any{v, v, v, v, v, v, v, v, v, v}}, 1, nil)
	}
	// Records are ~1.5 KB each; scale them up so a 4 MiB request needs several chunks.
	for i := range recs {
		l := make([]any, 400)
		for j := range l {
			l[j] = v
		}
		recs[i].Payload.After = opencdc.StructuredData{"l": l}
	}

	chunks := 0
	for from := 0; from < len(recs); {
		end, oversize := nextChunk(recs, from, writeLimit(maxReceive), maxReceive)
		is.True(!oversize)
		s2, s3 := encodedSizes(t, recs[from:end])
		is.True(s2 <= maxReceive && s3 <= maxReceive)
		chunks++
		from = end
	}
	is.True(chunks > 1)
}
