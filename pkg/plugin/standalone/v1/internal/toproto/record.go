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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	connectorv1 "go.buf.build/library/go-grpc/conduitio/conduit-plugin/connector/v1"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func Record(in record.Record) (*connectorv1.Record, error) {
	key, err := Data(in.Key)
	if err != nil {
		return nil, err
	}
	payload, err := Data(in.Payload)
	if err != nil {
		return nil, err
	}

	out := &connectorv1.Record{
		Position:  in.Position,
		Metadata:  in.Metadata,
		CreatedAt: timestamppb.New(in.CreatedAt),
		Key:       key,
		Payload:   payload,
	}
	return out, nil
}

func Data(in record.Data) (*connectorv1.Data, error) {
	if in == nil {
		return nil, nil
	}

	switch v := in.(type) {
	case record.RawData:
		return &connectorv1.Data{
			Data: &connectorv1.Data_RawData{
				RawData: v.Raw,
			},
		}, nil
	case record.StructuredData:
		data, err := structpb.NewStruct(v)
		if err != nil {
			return nil, err
		}
		return &connectorv1.Data{
			Data: &connectorv1.Data_StructuredData{
				StructuredData: data,
			},
		}, nil
	default:
		return nil, cerrors.Errorf("invalid Data type '%T'", in)
	}
}
