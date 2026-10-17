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
	"bytes"
	"context"
	"math"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	v2 "github.com/conduitio/conduit-connector-protocol/pconnector/v2"
	v3 "github.com/conduitio/conduit-connector-protocol/pconnector/v3"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

const (
	// twoTo53PlusOne is 2^53 + 1, the first integer a float64 rounds.
	twoTo53PlusOne = int64(9007199254740993)
	// maxUint64 does not fit in a float64 at all.
	maxUint64 = uint64(18446744073709551615)
)

// lossyStructuredData is a payload exercising every value kind that the v1/v2
// structpb wire format cannot carry exactly. See ADR
// 20261015-lossless-structured-values-protocol-v3.md and conduit#3100.
func lossyStructuredData() opencdc.StructuredData {
	return opencdc.StructuredData{
		"int64":   twoTo53PlusOne,
		"uint64":  maxUint64,
		"bytes":   []byte{0x00, 0x01, 0x02, 0xfe, 0xff},
		"nan":     math.NaN(),
		"pos_inf": math.Inf(1),
		"neg_inf": math.Inf(-1),
		"decimal": json.Number("12345678901234567890.12345678901234567890"),
	}
}

func lossyRecord() opencdc.Record {
	return opencdc.Record{
		Position:  opencdc.Position("position"),
		Operation: opencdc.OperationUpdate,
		Metadata:  opencdc.Metadata{"foo": "bar"},
		Key:       opencdc.RawData("key"),
		Payload: opencdc.Change{
			Before: opencdc.StructuredData{"before": int64(1)},
			After:  lossyStructuredData(),
		},
	}
}

// assertLosslessAfter proves the v3 wire format carried every value kind
// exactly, with its Go type intact - the v1/v2 structpb format would round,
// refuse, or stringify each of these.
func assertLosslessAfter(t *testing.T, after opencdc.Data) {
	t.Helper()
	is := is.New(t)

	sd, ok := after.(opencdc.StructuredData)
	is.True(ok) // after must still be structured data

	// int64 > 2^53 must come back as the exact int64, not a rounded float64.
	is.Equal(twoTo53PlusOne, sd["int64"])
	// uint64 must come back as the exact uint64.
	is.Equal(maxUint64, sd["uint64"])
	// []byte must come back as []byte, not a base64 string.
	rawBytes, ok := sd["bytes"].([]byte)
	is.True(ok)
	is.True(bytes.Equal([]byte{0x00, 0x01, 0x02, 0xfe, 0xff}, rawBytes))

	// NaN and ±Inf must come back as float64 values, not the strings the
	// structpb wire format produces.
	nan, ok := sd["nan"].(float64)
	is.True(ok)
	is.True(math.IsNaN(nan))
	posInf, ok := sd["pos_inf"].(float64)
	is.True(ok)
	is.True(math.IsInf(posInf, 1))
	negInf, ok := sd["neg_inf"].(float64)
	is.True(ok)
	is.True(math.IsInf(negInf, -1))

	// The decimal must come back as a json.Number with its exact literal form.
	is.Equal(json.Number("12345678901234567890.12345678901234567890"), sd["decimal"])
}

// TestProtocolV3_RoundTripLossless drives a record with every lossless value
// kind through the in-process pconnector harness negotiating v3, and asserts
// it comes back exact (type and value). It is the host-side counterpart to
// conduit-connector-protocol's own v3 round-trip test, proving the standalone
// Dispenser negotiates and converts v3, not just that the library does.
func TestProtocolV3_RoundTripLossless(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := zerolog.Nop()

	dispenser, _, mockSource, _ := newTestDispenser(t, logger, v3.Version)

	want := lossyRecord()
	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream pconnector.SourceRunStream) error {
			return stream.Server().Send(pconnector.SourceRunResponse{Records: []opencdc.Record{want}})
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	stream := source.NewStream()
	err = source.Run(ctx, stream)
	is.NoErr(err)

	rec, err := stream.Client().Recv()
	is.NoErr(err)
	is.Equal(1, len(rec.Records))

	got := rec.Records[0]
	is.Equal(want.Position, got.Position)
	is.Equal(want.Operation, got.Operation)
	is.Equal(want.Metadata, got.Metadata)
	is.Equal(want.Key, got.Key)
	assertLosslessAfter(t, got.Payload.After)
}

// TestProtocolV2_WideIntRefused proves the dual-ABI fallback: an old plugin
// that only negotiates v2 still gets the v0.21 wide-integer refusal - the
// record is refused loudly, never rounded and never silently dropped (ADR
// decision 6, conduit#3100). The server-side Send fails with
// opencdc.ErrIntegerNotRepresentable, and that refusal propagates to the
// host's Recv instead of a rounded value.
func TestProtocolV2_WideIntRefused(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	logger := zerolog.Nop()

	dispenser, _, mockSource, _ := newTestDispenser(t, logger, v2.Version)

	rec := opencdc.Record{
		Position:  opencdc.Position("position"),
		Operation: opencdc.OperationUpdate,
		Key:       opencdc.RawData("key"),
		Payload: opencdc.Change{
			After: opencdc.StructuredData{"int64": twoTo53PlusOne},
		},
	}

	mockSource.EXPECT().
		Run(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, stream pconnector.SourceRunStream) error {
			// The refusal happens here, on the v2 server's toproto conversion,
			// before the record ever reaches the wire.
			err := stream.Server().Send(pconnector.SourceRunResponse{Records: []opencdc.Record{rec}})
			is.True(cerrors.Is(err, opencdc.ErrIntegerNotRepresentable))
			return err
		})

	source, err := dispenser.DispenseSource()
	is.NoErr(err)

	stream := source.NewStream()
	err = source.Run(ctx, stream)
	is.NoErr(err)

	// The host must receive the refusal, not a rounded record.
	_, err = stream.Client().Recv()
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "would be rounded"))
}
