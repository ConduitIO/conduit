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
	"context"
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/schema"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/egress"
	"github.com/matryer/is"
	"github.com/tetratelabs/wazero"
)

// compileForTest compiles a guest on the shared test runtime and closes the
// compiled module when the test ends, so its heap is released instead of being
// held for the whole run. Tests in this package do not run in parallel, which
// the runtime requires for compilation (see TestMain).
func compileForTest(t *testing.T, binary []byte) wazero.CompiledModule {
	t.Helper()
	ctx := context.Background()
	m, err := TestRuntime.CompileModule(ctx, binary)
	if err != nil {
		t.Fatalf("compiling test guest: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(ctx) })
	return m
}

// wideIntRecord returns a record whose structured data holds values the v1
// wire shape cannot carry: a wide int64 (2^53+1), a full-range uint64, raw
// bytes and an exact decimal.
func wideIntRecord() opencdc.Record {
	return opencdc.Record{
		Operation: opencdc.OperationCreate,
		Key:       opencdc.StructuredData{"id": int64(9007199254740993)},
		Payload: opencdc.Change{
			After: opencdc.StructuredData{
				"id": int64(9007199254740993), // 2^53 + 1, refused by the v1 encoder
				"u":  uint64(18446744073709551615),
				"b":  []byte{0x00, 0x01, 0xfe, 0xff},
				"d":  json.Number("1.234567890123456789"),
			},
		},
	}
}

// TestWASMProcessor_StructuredValuesV2_RoundTripWideInt is the host-side proof
// of ADR 20261015-lossless-structured-values-protocol-v3.md item 4: a guest
// that declares structured_values_v2 receives opencdc.v2 records, and a wide
// integer (plus uint64, []byte and decimal) round-trips the WASM boundary
// exactly.
func TestWASMProcessor_StructuredValuesV2_RoundTripWideInt(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	underTest, err := newWASMProcessor(ctx, TestRuntime, compileForTest(t, EchoProcessorBinary), CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", log.Test(t))
	is.NoErr(err)

	// Specification reads the guest's capability. The echo guest is built
	// against this SDK, which always declares structured_values_v2.
	_, err = underTest.Specification()
	is.NoErr(err)
	is.True(underTest.structuredValuesV2)

	processed := underTest.Process(ctx, []opencdc.Record{wideIntRecord()})
	is.Equal(1, len(processed))

	got, ok := processed[0].(sdk.SingleRecord)
	is.True(ok)

	key := opencdc.Record(got).Key.(opencdc.StructuredData)
	is.Equal(key["id"], int64(9007199254740993))

	after := opencdc.Record(got).Payload.After.(opencdc.StructuredData)
	is.Equal(after["id"], int64(9007199254740993))
	is.Equal(after["u"], uint64(18446744073709551615))
	is.Equal(after["b"], []byte{0x00, 0x01, 0xfe, 0xff})
	is.Equal(after["d"], json.Number("1.234567890123456789"))

	is.NoErr(underTest.Teardown(ctx))
}

// TestWASMProcessor_OldProcessor_ReceivesV1AndGetsRefusal is the backward
// compatibility half of item 4: a processor built against an SDK that predates
// structured_values_v2 does not declare the capability, so the host keeps
// sending opencdc.v1 records — and a wide integer is refused at the v1
// boundary (never rounded, never sent as a v2 record).
func TestWASMProcessor_OldProcessor_ReceivesV1AndGetsRefusal(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	underTest, err := newWASMProcessor(ctx, TestRuntime, compileForTest(t, OldEchoProcessorBinary), CompiledHostModule, schema.NewInMemoryService(), egress.DenyAll(), "test-processor", log.Test(t))
	is.NoErr(err)

	// The old guest's Specify.Response lacks structured_values_v2, so the host
	// reads the capability as false.
	_, err = underTest.Specification()
	is.NoErr(err)
	is.True(!underTest.structuredValuesV2)

	processed := underTest.Process(ctx, []opencdc.Record{wideIntRecord()})
	is.Equal(1, len(processed))

	// The refusal happens host-side, before the command is sent, so the guest
	// never sees a record — and certainly never a v2 record.
	errRec, ok := processed[0].(sdk.ErrorRecord)
	is.True(ok)
	is.True(errRec.Error != nil)
	is.True(strings.Contains(errRec.Error.Error(), "safe integer range"))

	is.NoErr(underTest.Teardown(ctx))
}
