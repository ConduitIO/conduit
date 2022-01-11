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

package plugins

import (
	"log"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins/proto"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/conduitio/conduit/pkg/record/schema"
	protoSchema "github.com/conduitio/conduit/pkg/record/schema/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func toInternalConfig(cfg *proto.Config) Config {
	return Config{Settings: cfg.Values}
}

func toInternalRecord(resp *proto.Record) (record.Record, error) {
	payload, err := toInternalRecordPayload(resp)
	if err != nil {
		return record.Record{}, cerrors.Errorf("could not get payload: %w", err)
	}

	key, err := toInternalRecordKey(resp)
	if err != nil {
		return record.Record{}, cerrors.Errorf("could not get key: %w", err)
	}

	r := record.Record{
		Position:  resp.Position.Position,
		Metadata:  resp.Metadata.Values,
		CreatedAt: toInternalRecordTimestamp(resp),
		Key:       key,
		Payload:   payload,
	}

	return r, nil
}

func toInternalRecordTimestamp(r *proto.Record) time.Time {
	if !r.GetCreatedAt().IsValid() {
		// TODO log with internal logger
		log.Println("record timestamp invalid, falling back to now")
		return time.Now().UTC()
	}
	return r.GetCreatedAt().AsTime()
}

func toInternalRecordKey(r *proto.Record) (record.Data, error) {
	if r.GetKey() == nil {
		return nil, nil
	}
	switch v := r.GetKey().(type) {
	case *proto.Record_KeyRaw:
		s, err := toInternalSchema(v.KeyRaw)
		if err != nil {
			return nil, err
		}
		return record.RawData{
			Raw:    v.KeyRaw.Raw,
			Schema: s,
		}, nil
	case *proto.Record_KeyStructured:
		return record.StructuredData(v.KeyStructured.AsMap()), nil
	}
	return nil, cerrors.Errorf("unexpected key type %T", r.GetKey())
}

func toInternalRecordPayload(r *proto.Record) (record.Data, error) {
	if r.GetPayload() == nil {
		return nil, nil
	}
	switch v := r.GetPayload().(type) {
	case *proto.Record_PayloadRaw:
		s, err := toInternalSchema(v.PayloadRaw)
		if err != nil {
			return nil, err
		}
		return record.RawData{
			Raw:    v.PayloadRaw.Raw,
			Schema: s,
		}, nil
	case *proto.Record_PayloadStructured:
		return record.StructuredData(v.PayloadStructured.AsMap()), nil
	}
	return nil, cerrors.Errorf("unexpected payload type %T", r.GetPayload())
}

func toInternalSchema(cr *proto.RawData) (schema.Schema, error) {
	if cr.GetSchema() == nil {
		return nil, nil
	}
	if v, ok := cr.GetSchema().(*proto.RawData_ProtobufSchema_); ok {
		return protoSchema.NewSchema(v.ProtobufSchema.GetFileDescriptorSet(), "", 1)
	}
	// TODO case *proto.RawData_AvroSchema:
	// TODO case *proto.RawData_JsonSchema:
	return nil, cerrors.Errorf("unexpected schema type %T", cr.GetSchema())
}

func toProtoRecord(r record.Record) (*proto.Record, error) {
	p := &proto.Record{
		Position: &proto.Position{Position: r.Position},
		Metadata: &proto.Metadata{
			Values: r.Metadata,
		},
		CreatedAt: timestamppb.New(r.CreatedAt),
	}
	err := setProtoRecordKey(r.Key, p)
	if err != nil {
		return nil, cerrors.Errorf("could not set proto record key: %w", err)
	}
	err = setProtoRecordPayload(r.Payload, p)
	if err != nil {
		return nil, cerrors.Errorf("could not set proto record payload: %w", err)
	}

	return p, nil
}

func setProtoConfig(c Config, cfg *proto.Config) {
	cfg.Values = c.Settings
}

func setProtoRecordKey(c record.Data, r *proto.Record) error {
	if c == nil {
		return nil
	}

	switch v := c.(type) {
	case record.StructuredData:
		content, err := structpb.NewStruct(v)
		if err != nil {
			return err
		}
		r.Key = &proto.Record_KeyStructured{KeyStructured: content}
	case record.RawData:
		cr := &proto.RawData{Raw: v.Raw}
		err := setProtoSchema(v.Schema, cr)
		if err != nil {
			return err
		}
		r.Key = &proto.Record_KeyRaw{KeyRaw: cr}
	default:
		return cerrors.Errorf("unexpected content type %T", c)
	}
	return nil
}

func setProtoRecordPayload(c record.Data, r *proto.Record) error {
	if c == nil {
		return nil
	}

	switch v := c.(type) {
	case record.StructuredData:
		content, err := structpb.NewStruct(v)
		if err != nil {
			return err
		}
		r.Payload = &proto.Record_PayloadStructured{PayloadStructured: content}
	case record.RawData:
		cr := &proto.RawData{Raw: v.Raw}
		err := setProtoSchema(v.Schema, cr)
		if err != nil {
			return err
		}
		r.Payload = &proto.Record_PayloadRaw{PayloadRaw: cr}
	default:
		return cerrors.Errorf("unexpected content type %T", c)
	}
	return nil
}

func setProtoSchema(s schema.Schema, cr *proto.RawData) error {
	if s == nil {
		return nil
	}

	switch v := s.(type) {
	case protoSchema.Schema:
		cr.Schema = &proto.RawData_ProtobufSchema_{
			ProtobufSchema: &proto.RawData_ProtobufSchema{
				Version:           int32(v.Version()),
				FileDescriptorSet: v.FileDescriptorSet(),
			},
		}
	// TODO case avroSchema.Schema:
	// TODO case jsonSchema.Schema:
	default:
		return cerrors.Errorf("unexpected schema type %T", s)
	}
	return nil
}
