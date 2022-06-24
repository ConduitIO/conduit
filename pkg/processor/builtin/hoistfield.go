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
	hoistFieldKeyName     = "hoistfieldkey"
	hoistFieldPayloadName = "hoistfieldpayload"

	hoistFieldConfigField = "field"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(hoistFieldKeyName, HoistFieldKey)
	processor.GlobalBuilderRegistry.MustRegister(hoistFieldPayloadName, HoistFieldPayload)
}

// HoistFieldKey builds the following transform:
//  * If the key is raw and has a schema attached, wrap it using the specified
//    field name in a struct.
//  * If the key is raw and has no schema, transform it into structured data by
//    creating a map with the hoisted field and raw data as the value.
//  * If the key is structured, wrap it using the specified field name in a map.
func HoistFieldKey(config processor.Config) (processor.Processor, error) {
	return hoistField(hoistFieldKeyName, recordKeyGetSetter{}, config)
}

// HoistFieldPayload builds the following transformation:
//  * If the payload is raw and has a schema attached, wrap it using the
//    specified field name in a struct.
//  * If the payload is raw and has no schema, transform it into structured data
//    by creating a map with the hoisted field and raw data as the value.
//  * If the payload is structured, wrap it using the specified field name in a
//    map.
func HoistFieldPayload(config processor.Config) (processor.Processor, error) {
	return hoistField(hoistFieldPayloadName, recordPayloadGetSetter{}, config)
}

func hoistField(
	transformName string,
	getSetter recordDataGetSetter,
	config processor.Config,
) (processor.Processor, error) {
	var (
		err       error
		fieldName string
	)

	if fieldName, err = getConfigFieldString(config, hoistFieldConfigField); err != nil {
		return nil, cerrors.Errorf("%s: %w", transformName, err)
	}

	return ProcessorFunc(func(_ context.Context, r record.Record) (record.Record, error) {
		data := getSetter.Get(r)

		switch d := data.(type) {
		case record.RawData:
			if d.Schema == nil {
				data = record.StructuredData{
					fieldName: d.Raw,
				}
			} else {
				return record.Record{}, cerrors.Errorf("%s: data with schema not supported yet", transformName) // TODO
			}
		case record.StructuredData:
			data = record.StructuredData{
				fieldName: map[string]interface{}(d),
			}
		default:
			return record.Record{}, cerrors.Errorf("%s: unexpected data type %T", transformName, data)
		}

		r = getSetter.Set(r, data)
		return r, nil
	}), nil
}
