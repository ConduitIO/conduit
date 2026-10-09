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

package builtin

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	conduitschemaregistry "github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/matryer/is"
)

// fixedProcessor returns out from Process.
type fixedProcessor struct {
	sdk.UnimplementedProcessor
	out []sdk.ProcessedRecord
}

func (p fixedProcessor) Process(context.Context, []opencdc.Record) []sdk.ProcessedRecord {
	return p.out
}

// TestProcessorWithID_ClassifiesAvroLimit: the processor-sdk schema decode
// middleware applied to built-in processors returns plain errors; the
// wrapper tags an Avro element-limit failure with
// schema.avro.limit_exceeded and leaves other results alone.
func TestProcessorWithID_ClassifiesAvroLimit(t *testing.T) {
	is := is.New(t)

	limitErr := cerrors.New("record 0: failed to decode payload: failed to unmarshal bytes with schema: " +
		"could not unmarshal from avro: avro: decode map: size is greater than `Config.MaxMapAllocSize`")
	otherErr := cerrors.New("boom")
	single := sdk.SingleRecord{Position: opencdc.Position("p")}

	p := newProcessorWithID(fixedProcessor{out: []sdk.ProcessedRecord{
		sdk.ErrorRecord{Error: limitErr},
		sdk.ErrorRecord{Error: otherErr},
		single,
	}}, "proc")
	got := p.Process(context.Background(), nil)
	is.Equal(len(got), 3)

	ce, ok := conduiterr.Get(got[0].(sdk.ErrorRecord).Error)
	is.True(ok)
	is.Equal(ce.Code, conduitschemaregistry.CodeAvroLimitExceeded)
	is.True(cerrors.Is(got[0].(sdk.ErrorRecord).Error, limitErr))

	is.Equal(got[1].(sdk.ErrorRecord).Error, otherErr)
	is.Equal(got[2], sdk.ProcessedRecord(single))
}
