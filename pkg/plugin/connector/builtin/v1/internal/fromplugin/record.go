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

package fromplugin

import (
	"fmt"

	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
)

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var cTypes [1]struct{}
	_ = cTypes[int(record.OperationCreate)-int(cpluginv1.OperationCreate)]
	_ = cTypes[int(record.OperationUpdate)-int(cpluginv1.OperationUpdate)]
	_ = cTypes[int(record.OperationDelete)-int(cpluginv1.OperationDelete)]
	_ = cTypes[int(record.OperationSnapshot)-int(cpluginv1.OperationSnapshot)]
}

func Record(in cpluginv1.Record) (record.Record, error) {
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

func Change(in cpluginv1.Change) (record.Change, error) {
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

func Data(in cpluginv1.Data) (record.Data, error) {
	if in == nil {
		return nil, nil
	}

	switch v := in.(type) {
	case cpluginv1.RawData:
		return record.RawData{
			Raw: v,
		}, nil
	case cpluginv1.StructuredData:
		return record.StructuredData(v), nil
	default:
		return nil, cerrors.Errorf("invalid Data type '%T'", in)
	}
}
