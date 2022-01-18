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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	connectorv1 "go.buf.build/library/go-grpc/conduitio/conduit-plugin/connector/v1"
)

func Record(in *connectorv1.Record) (record.Record, error) {
	key, err := Data(in.Key)
	if err != nil {
		return record.Record{}, err
	}
	payload, err := Data(in.Payload)
	if err != nil {
		return record.Record{}, err
	}

	out := record.Record{
		Position:  in.Position,
		Metadata:  in.Metadata,
		SourceID:  "",
		CreatedAt: in.CreatedAt.AsTime(),
		ReadAt:    time.Now().UTC(),
		Key:       key,
		Payload:   payload,
	}
	return out, nil
}

func Data(in *connectorv1.Data) (record.Data, error) {
	d := in.GetData()
	if d == nil {
		return nil, nil
	}

	switch v := d.(type) {
	case *connectorv1.Data_RawData:
		return record.RawData{
			Raw:    v.RawData,
			Schema: nil,
		}, nil
	case *connectorv1.Data_StructuredData:
		return record.StructuredData(v.StructuredData.AsMap()), nil
	default:
		return nil, cerrors.New("invalid Data type")
	}
}
