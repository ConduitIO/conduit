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

package procbuiltin

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
)

const (
	insertFieldKeyProcType     = "insertfieldkey"
	insertFieldPayloadProcType = "insertfieldpayload"

	insertFieldConfigStaticField   = "static.field"
	insertFieldConfigStaticValue   = "static.value"
	insertFieldConfigPositionField = "position.field"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(insertFieldKeyProcType, InsertFieldKey)
	processor.GlobalBuilderRegistry.MustRegister(insertFieldPayloadProcType, InsertFieldPayload)
}

// InsertFieldKey builds the following processor:
//   - If the key is raw and has a schema attached, insert the field(s) in the
//     key data.
//   - If the key is raw and has no schema, return an error (not supported).
//   - If the key is structured, set the field(s) in the key data.
func InsertFieldKey(config processor.Config) (processor.Interface, error) {
	return insertField(insertFieldKeyProcType, recordKeyGetSetter{}, config)
}

// InsertFieldPayload builds the same processor as InsertFieldKey, except that
// it operates on the field Record.Payload.After.
func InsertFieldPayload(config processor.Config) (processor.Interface, error) {
	return insertField(insertFieldPayloadProcType, recordPayloadGetSetter{}, config)
}

func insertField(
	processorType string,
	getSetter recordDataGetSetter,
	config processor.Config,
) (processor.Interface, error) {
	var (
		err error

		staticFieldName  string
		staticFieldValue string
		positionField    string
	)

	positionField = config.Settings[insertFieldConfigPositionField]
	staticFieldName, ok := config.Settings[insertFieldConfigStaticField]
	if ok {
		if staticFieldValue, err = getConfigFieldString(config, insertFieldConfigStaticValue); err != nil {
			return nil, cerrors.Errorf("%s: %w", processorType, err)
		}
	}
	if staticFieldName == "" && positionField == "" {
		return nil, cerrors.Errorf("%s: no fields configured to be inserted", processorType)
	}

	return NewFuncWrapper(func(_ context.Context, r record.Record) (record.Record, error) {
		data := getSetter.Get(r)

		switch d := data.(type) {
		case record.RawData:
			if d.Schema == nil {
				return record.Record{}, cerrors.Errorf("%s: schemaless raw data not supported", processorType)
			}
			return record.Record{}, cerrors.Errorf("%s: data with schema not supported yet", processorType) // TODO
		case record.StructuredData:
			// TODO add support for nested fields
			if staticFieldName != "" {
				d[staticFieldName] = staticFieldValue
			}
			if positionField != "" {
				d[positionField] = r.Position
			}
		default:
			return record.Record{}, cerrors.Errorf("%s: unexpected data type %T", processorType, data)
		}

		r = getSetter.Set(r, data)
		return r, nil
	}), nil
}
