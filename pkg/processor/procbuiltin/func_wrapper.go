// Copyright © 2023 Meroxa, Inc.
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

package procbuiltin

import (
	"context"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/processor"

	"github.com/conduitio/conduit/pkg/record"
)

// FuncWrapper is an adapter allowing use of a function as a processor.Interface.
// todo think about moving to the processor sdk for very simple processors
type FuncWrapper struct {
	sdk.UnimplementedProcessor

	f func(context.Context, record.Record) (record.Record, error)
}

func NewFuncWrapper(f func(context.Context, record.Record) (record.Record, error)) FuncWrapper {
	return FuncWrapper{f: f}
}

func (f FuncWrapper) Specification() (sdk.Specification, error) {
	//TODO implement me
	panic("implement me")
}

func (f FuncWrapper) Configure(ctx context.Context, m map[string]string) error {
	//TODO implement me
	panic("implement me")
}

func (f FuncWrapper) Open(ctx context.Context) error {
	//TODO implement me
	panic("implement me")
}

func (f FuncWrapper) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	outRecs := make([]sdk.ProcessedRecord, len(records))
	for i, inRec := range records {
		outRec, err := f.f(ctx, f.toConduitRecord(inRec))
		if cerrors.Is(err, processor.ErrSkipRecord) {
			outRecs[i] = sdk.FilterRecord{}
		} else if err != nil {
			outRecs[i] = sdk.ErrorRecord{Error: err}
		} else {
			outRecs[i] = f.toSingleRecord(outRec)
		}
	}

	return outRecs
}

func (f FuncWrapper) Teardown(ctx context.Context) error {
	//TODO implement me
	panic("implement me")
}

func (f FuncWrapper) InspectIn(ctx context.Context, id string) *inspector.Session {
	//TODO implement me
	panic("implement me")
}

func (f FuncWrapper) InspectOut(ctx context.Context, id string) *inspector.Session {
	//TODO implement me
	panic("implement me")
}

func (f FuncWrapper) toSingleRecord(rec record.Record) sdk.ProcessedRecord {
	return sdk.SingleRecord{}
}

func (f FuncWrapper) toConduitRecord(rec opencdc.Record) record.Record {
	return record.Record{}
}
