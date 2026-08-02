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

package upgrade

import (
	"context"
	"fmt"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
)

// filterAtPosProcessor is a stream.Processor that filters every record whose
// position is in the given set - v1's per-record equivalent of
// filterAtTask.
type filterAtPosProcessor struct {
	positions map[string]bool
}

func (p *filterAtPosProcessor) Open(context.Context) error     { return nil }
func (p *filterAtPosProcessor) Teardown(context.Context) error { return nil }
func (p *filterAtPosProcessor) Process(_ context.Context, recs []opencdc.Record) []sdk.ProcessedRecord {
	r := recs[0] // v1 always calls Process with exactly one record.
	if p.positions[string(r.Position)] {
		return []sdk.ProcessedRecord{sdk.FilterRecord{}}
	}
	return []sdk.ProcessedRecord{sdk.SingleRecord(r)}
}

// errorAtPosProcessor is a stream.Processor that nacks (routes to the DLQ,
// or halts, depending on DLQ policy) every record whose position is in the
// given set - v1's per-record equivalent of nackAtTask.
type errorAtPosProcessor struct {
	positions map[string]bool
}

func (p *errorAtPosProcessor) Open(context.Context) error     { return nil }
func (p *errorAtPosProcessor) Teardown(context.Context) error { return nil }
func (p *errorAtPosProcessor) Process(_ context.Context, recs []opencdc.Record) []sdk.ProcessedRecord {
	r := recs[0]
	if p.positions[string(r.Position)] {
		return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: fmt.Errorf("synthetic nack at position %s", r.Position)}}
	}
	return []sdk.ProcessedRecord{sdk.SingleRecord(r)}
}

// filterOrErrorAtPosProcessor filters records at one set of positions and
// errors (routes to the DLQ, or halts) records at another - v1's
// counterpart to the v2 filter+nack combination (see combos_test.go's
// TestV1Combo_FilterNack).
type filterOrErrorAtPosProcessor struct {
	filterPositions map[string]bool
	errorPositions  map[string]bool
}

func (p *filterOrErrorAtPosProcessor) Open(context.Context) error     { return nil }
func (p *filterOrErrorAtPosProcessor) Teardown(context.Context) error { return nil }
func (p *filterOrErrorAtPosProcessor) Process(_ context.Context, recs []opencdc.Record) []sdk.ProcessedRecord {
	r := recs[0]
	switch {
	case p.filterPositions[string(r.Position)]:
		return []sdk.ProcessedRecord{sdk.FilterRecord{}}
	case p.errorPositions[string(r.Position)]:
		return []sdk.ProcessedRecord{sdk.ErrorRecord{Error: fmt.Errorf("synthetic nack at position %s", r.Position)}}
	default:
		return []sdk.ProcessedRecord{sdk.SingleRecord(r)}
	}
}

// wrongCountAtPosProcessor returns ZERO records (instead of the required
// one) for the record at the given position - the only shape "a processor
// returned fewer records than it received" can take in v1, since v1 always
// calls Process with exactly one record (see
// pkg/lifecycle/stream/processor.go's ProcessorNode.Run: "recsIn :=
// []opencdc.Record{msg.Record}"). Unlike funnel/v2's RecordFlagRetry, v1 has
// no batch-partial-response concept to retry - ProcessorNode.Run's own
// length check (len(recsIn) != len(recsOut)) makes this immediately FATAL,
// never a silent skip. See shapes_v1_test.go's TestV1_WrongCountIsFatalNotSilentRetry.
type wrongCountAtPosProcessor struct {
	targetPos string
}

func (p *wrongCountAtPosProcessor) Open(context.Context) error     { return nil }
func (p *wrongCountAtPosProcessor) Teardown(context.Context) error { return nil }
func (p *wrongCountAtPosProcessor) Process(_ context.Context, recs []opencdc.Record) []sdk.ProcessedRecord {
	r := recs[0]
	if string(r.Position) == p.targetPos {
		return []sdk.ProcessedRecord{} // wrong count: 0 instead of 1
	}
	return []sdk.ProcessedRecord{sdk.SingleRecord(r)}
}

// multiRecordAtPosProcessor returns a fan-out sdk.MultiRecord result (N
// records from 1 input) for the record at the given position - v1's only
// possible representation of both the "split" and "fan-out" shapes (see
// pkg/lifecycle/stream/processor.go's handleProcessedRecord: ANY
// sdk.MultiRecord response is fatal on the classic engine, regardless of its
// length or of how many destinations are configured - see
// CodeFanOutRequiresArchV2 and shapes_v1_test.go's
// TestV1_SplitAndFanOut_RefusedLoudlyBeforeAnyWrite).
type multiRecordAtPosProcessor struct {
	targetPos string
	into      int
}

func (p *multiRecordAtPosProcessor) Open(context.Context) error     { return nil }
func (p *multiRecordAtPosProcessor) Teardown(context.Context) error { return nil }
func (p *multiRecordAtPosProcessor) Process(_ context.Context, recs []opencdc.Record) []sdk.ProcessedRecord {
	r := recs[0]
	if string(r.Position) == p.targetPos {
		pieces := make([]opencdc.Record, p.into)
		for i := range pieces {
			pieces[i] = r
		}
		return []sdk.ProcessedRecord{sdk.MultiRecord(pieces)}
	}
	return []sdk.ProcessedRecord{sdk.SingleRecord(r)}
}
