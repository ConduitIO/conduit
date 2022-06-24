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

package builtin

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
)

const (
	insertFieldKeyName     = "insertfieldkey"
	insertFieldPayloadName = "insertfieldpayload"

	insertFieldConfigStaticField    = "static.field"
	insertFieldConfigStaticValue    = "static.value"
	insertFieldConfigTimestampField = "timestamp.field"
	insertFieldConfigPositionField  = "position.field"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(insertFieldKeyName, InsertFieldKey)
	processor.GlobalBuilderRegistry.MustRegister(insertFieldPayloadName, InsertFieldPayload)
}

// InsertFieldKey builds the following transform:
//  * If the key is raw and has a schema attached, insert the field(s) in the
//    key data.
//  * If the key is raw and has no schema, return an error (not supported).
//  * If the key is structured, set the field(s) in the key data.
func InsertFieldKey(config processor.Config) (processor.Processor, error) {
	return insertField(insertFieldKeyName, recordKeyGetSetter{}, config)
}

// InsertFieldPayload builds the following transformation:
//  * If the payload is raw and has a schema attached, insert the field(s) in
//    the payload data.
//  * If the payload is raw and has no schema, return an error (not supported).
//  * If the payload is structured, set the field(s) in the payload data.
func InsertFieldPayload(config processor.Config) (processor.Processor, error) {
	return insertField(insertFieldPayloadName, recordPayloadGetSetter{}, config)
}

func insertField(
	transformName string,
	getSetter recordDataGetSetter,
	config processor.Config,
) (processor.Processor, error) {
	var (
		err error

		staticFieldName  string
		staticFieldValue string
		timestampField   string
		positionField    string
	)

	timestampField = config.Settings[insertFieldConfigTimestampField]
	positionField = config.Settings[insertFieldConfigPositionField]
	staticFieldName, ok := config.Settings[insertFieldConfigStaticField]
	if ok {
		if staticFieldValue, err = getConfigFieldString(config, insertFieldConfigStaticValue); err != nil {
			return nil, cerrors.Errorf("%s: %w", transformName, err)
		}
	}
	if staticFieldName == "" && timestampField == "" && positionField == "" {
		return nil, cerrors.Errorf("%s: no fields configured to be inserted", transformName)
	}

	return ProcessorFunc(func(_ context.Context, r record.Record) (record.Record, error) {
		data := getSetter.Get(r)

		switch d := data.(type) {
		case record.RawData:
			if d.Schema == nil {
				return record.Record{}, cerrors.Errorf("%s: schemaless raw data not supported", transformName)
			}
			return record.Record{}, cerrors.Errorf("%s: data with schema not supported yet", transformName) // TODO
		case record.StructuredData:
			// TODO add support for nested fields
			if staticFieldName != "" {
				d[staticFieldName] = staticFieldValue
			}
			if timestampField != "" {
				d[timestampField] = r.CreatedAt
			}
			if positionField != "" {
				d[positionField] = r.Position
			}
		default:
			return record.Record{}, cerrors.Errorf("%s: unexpected data type %T", transformName, data)
		}

		r = getSetter.Set(r, data)
		return r, nil
	}), nil
}
