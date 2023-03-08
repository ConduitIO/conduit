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

package toproto

import (
	"fmt"

	opencdcv1 "github.com/conduitio/conduit-connector-protocol/proto/gen/opencdc/v1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	"google.golang.org/protobuf/types/known/structpb"
)

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var cTypes [1]struct{}
	_ = cTypes[int(record.OperationCreate)-int(opencdcv1.Operation_OPERATION_CREATE)]
	_ = cTypes[int(record.OperationUpdate)-int(opencdcv1.Operation_OPERATION_UPDATE)]
	_ = cTypes[int(record.OperationDelete)-int(opencdcv1.Operation_OPERATION_DELETE)]
	_ = cTypes[int(record.OperationSnapshot)-int(opencdcv1.Operation_OPERATION_SNAPSHOT)]
}

func Record(in record.Record) (*opencdcv1.Record, error) {
	key, err := Data(in.Key)
	if err != nil {
		return nil, err
	}
	payload, err := Change(in.Payload)
	if err != nil {
		return nil, err
	}

	out := &opencdcv1.Record{
		Position:  in.Position,
		Operation: opencdcv1.Operation(in.Operation),
		Metadata:  in.Metadata,
		Key:       key,
		Payload:   payload,
	}
	return out, nil
}

func Change(in record.Change) (*opencdcv1.Change, error) {
	before, err := Data(in.Before)
	if err != nil {
		return nil, fmt.Errorf("error converting before: %w", err)
	}

	after, err := Data(in.After)
	if err != nil {
		return nil, fmt.Errorf("error converting after: %w", err)
	}

	out := opencdcv1.Change{
		Before: before,
		After:  after,
	}
	return &out, nil
}

func Data(in record.Data) (*opencdcv1.Data, error) {
	if in == nil {
		return nil, nil
	}

	switch v := in.(type) {
	case record.RawData:
		return &opencdcv1.Data{
			Data: &opencdcv1.Data_RawData{
				RawData: v.Raw,
			},
		}, nil
	case record.StructuredData:
		data, err := structpb.NewStruct(v)
		if err != nil {
			return nil, err
		}
		return &opencdcv1.Data{
			Data: &opencdcv1.Data_StructuredData{
				StructuredData: data,
			},
		}, nil
	default:
		return nil, cerrors.Errorf("invalid Data type '%T'", in)
	}
}
