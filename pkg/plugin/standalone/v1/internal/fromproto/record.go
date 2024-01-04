// Copyright © 2022 Meroxa, Inc.
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

package fromproto

import (
	"fmt"

	opencdcv1 "github.com/conduitio/conduit-commons/proto/opencdc/v1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
)

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var cTypes [1]struct{}
	_ = cTypes[int(record.OperationCreate)-int(opencdcv1.Operation_OPERATION_CREATE)]
	_ = cTypes[int(record.OperationUpdate)-int(opencdcv1.Operation_OPERATION_UPDATE)]
	_ = cTypes[int(record.OperationDelete)-int(opencdcv1.Operation_OPERATION_DELETE)]
	_ = cTypes[int(record.OperationSnapshot)-int(opencdcv1.Operation_OPERATION_SNAPSHOT)]
}

func Record(in *opencdcv1.Record) (record.Record, error) {
	key, err := Data(in.Key)
	if err != nil {
		return record.Record{}, err
	}
	payload, err := Change(in.Payload)
	if err != nil {
		return record.Record{}, err
	}

	out := record.Record{
		Position:  in.Position,
		Operation: record.Operation(in.Operation),
		Metadata:  in.Metadata,
		Key:       key,
		Payload:   payload,
	}
	return out, nil
}

func Change(in *opencdcv1.Change) (record.Change, error) {
	before, err := Data(in.Before)
	if err != nil {
		return record.Change{}, fmt.Errorf("error converting before: %w", err)
	}

	after, err := Data(in.After)
	if err != nil {
		return record.Change{}, fmt.Errorf("error converting after: %w", err)
	}

	out := record.Change{
		Before: before,
		After:  after,
	}
	return out, nil
}

func Data(in *opencdcv1.Data) (record.Data, error) {
	d := in.GetData()
	if d == nil {
		return nil, nil
	}

	switch v := d.(type) {
	case *opencdcv1.Data_RawData:
		return record.RawData{
			Raw:    v.RawData,
			Schema: nil,
		}, nil
	case *opencdcv1.Data_StructuredData:
		return record.StructuredData(v.StructuredData.AsMap()), nil
	default:
		return nil, cerrors.Errorf("invalid Data type '%T'", d)
	}
}
