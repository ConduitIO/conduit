// Copyright © 2024 Meroxa, Inc.
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
	"fmt"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	opencdcv1 "github.com/conduitio/conduit-commons/proto/opencdc/v1"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit-processor-sdk/pprocutils"
	processorv1 "github.com/conduitio/conduit-processor-sdk/proto/processor/v1"
)

// protoConverter converts between the SDK and protobuf types.
type protoConverter struct{}

func (c protoConverter) specification(resp *processorv1.Specify_Response) (sdk.Specification, error) {
	params := make(config.Parameters, len(resp.Parameters))
	err := params.FromProto(resp.Parameters)
	if err != nil {
		return sdk.Specification{}, err
	}

	return sdk.Specification{
		Name:        resp.Name,
		Summary:     resp.Summary,
		Description: resp.Description,
		Version:     resp.Version,
		Author:      resp.Author,
		Parameters:  params,
	}, nil
}

func (c protoConverter) records(in []opencdc.Record) ([]*opencdcv1.Record, error) {
	if in == nil {
		return nil, nil
	}

	out := make([]*opencdcv1.Record, len(in))
	for i, r := range in {
		out[i] = &opencdcv1.Record{}
		err := r.ToProto(out[i])
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (c protoConverter) processedRecords(in []*processorv1.Process_ProcessedRecord) ([]sdk.ProcessedRecord, error) {
	if in == nil {
		return nil, nil
	}

	out := make([]sdk.ProcessedRecord, len(in))
	var err error
	for i, r := range in {
		out[i], err = c.processedRecord(r)
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (c protoConverter) processedRecord(in *processorv1.Process_ProcessedRecord) (sdk.ProcessedRecord, error) {
	if in == nil || in.Record == nil {
		return nil, nil
	}

	switch v := in.Record.(type) {
	case *processorv1.Process_ProcessedRecord_SingleRecord:
		return c.singleRecord(v)
	case *processorv1.Process_ProcessedRecord_FilterRecord:
		return c.filterRecord(v)
	case *processorv1.Process_ProcessedRecord_ErrorRecord:
		return c.errorRecord(v)
	default:
		return nil, fmt.Errorf("unknown processed record type: %T", in.Record)
	}
}

func (c protoConverter) singleRecord(in *processorv1.Process_ProcessedRecord_SingleRecord) (sdk.SingleRecord, error) {
	if in == nil {
		return sdk.SingleRecord{}, nil
	}

	var rec opencdc.Record
	err := rec.FromProto(in.SingleRecord)
	if err != nil {
		return sdk.SingleRecord{}, err
	}

	return sdk.SingleRecord(rec), nil
}

func (c protoConverter) filterRecord(_ *processorv1.Process_ProcessedRecord_FilterRecord) (sdk.FilterRecord, error) {
	return sdk.FilterRecord{}, nil
}

func (c protoConverter) errorRecord(in *processorv1.Process_ProcessedRecord_ErrorRecord) (sdk.ErrorRecord, error) {
	if in == nil || in.ErrorRecord == nil || in.ErrorRecord.Error == nil {
		return sdk.ErrorRecord{}, nil
	}
	return sdk.ErrorRecord{Error: c.error(in.ErrorRecord.Error)}, nil
}

func (c protoConverter) error(e *processorv1.Error) error {
	return pprocutils.NewError(e.Code, e.Message)
}
