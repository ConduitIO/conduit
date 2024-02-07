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

package builtin

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
// todo implement methods
type FuncWrapper struct {
	sdk.UnimplementedProcessor

	name string
	f    func(context.Context, record.Record) (record.Record, error)
}

func NewFuncWrapper(f func(context.Context, record.Record) (record.Record, error)) FuncWrapper {
	return FuncWrapper{f: f}
}

func (f FuncWrapper) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        f.name,
		Summary:     "",
		Description: "",
		Version:     "",
		Author:      "",
		Parameters:  nil,
	}, nil
}

func (f FuncWrapper) Configure(context.Context, map[string]string) error {
	return nil
}

func (f FuncWrapper) Open(context.Context) error {
	return nil
}

func (f FuncWrapper) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	outRecs := make([]sdk.ProcessedRecord, len(records))
	for i, inRec := range records {
		outRec, err := f.f(ctx, record.FromOpenCDC(inRec))
		switch {
		case cerrors.Is(err, processor.ErrSkipRecord):
			outRecs[i] = sdk.FilterRecord{}
		case err != nil:
			outRecs[i] = sdk.ErrorRecord{Error: err}
		default:
			outRecs[i] = sdk.SingleRecord(outRec.ToOpenCDC())
		}
	}

	return outRecs
}

func (f FuncWrapper) Teardown(context.Context) error {
	return nil
}

func (f FuncWrapper) InspectIn(context.Context, string) *inspector.Session {
	// TODO implement me
	panic("implement me")
}

func (f FuncWrapper) InspectOut(context.Context, string) *inspector.Session {
	// TODO implement me
	panic("implement me")
}

func (f FuncWrapper) Close() {

}
