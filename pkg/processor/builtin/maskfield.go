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
	"reflect"
	"strconv"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
)

const (
	maskFieldKeyName     = "maskfieldkey"
	maskFieldPayloadName = "maskfieldpayload"

	maskFieldConfigField       = "field"
	maskFieldConfigReplacement = "replacement"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(maskFieldKeyName, MaskFieldKey)
	processor.GlobalBuilderRegistry.MustRegister(maskFieldPayloadName, MaskFieldPayload)
}

// MaskFieldKey builds the following transform:
//  * If the key is raw and has a schema attached, replace the field with the
//    zero value of the fields type.
//  * If the key is raw and has no schema, return an error (not supported).
//  * If the key is structured, replace the field with the zero value of the
//    fields type.
func MaskFieldKey(config processor.Config) (processor.Processor, error) {
	return maskField(maskFieldKeyName, recordKeyGetSetter{}, config)
}

// MaskFieldPayload builds the following transformation:
//  * If the payload is raw and has a schema attached, replace the field with
//    the zero value of the fields type.
//  * If the payload is raw and has no schema, return an error (not supported).
//  * If the payload is structured, replace the field with the zero value of the
//    fields type.
func MaskFieldPayload(config processor.Config) (processor.Processor, error) {
	return maskField(maskFieldPayloadName, recordPayloadGetSetter{}, config)
}

func maskField(
	transformName string,
	getSetter recordDataGetSetter,
	config processor.Config,
) (processor.Processor, error) {
	var (
		err         error
		fieldName   string
		replacement string
	)

	if fieldName, err = getConfigFieldString(config, maskFieldConfigField); err != nil {
		return nil, cerrors.Errorf("%s: %w", transformName, err)
	}
	replacement = config.Settings[maskFieldConfigReplacement]

	return funcProcessor{
		fn: func(_ context.Context, r record.Record) (record.Record, error) {
			data := getSetter.Get(r)

			switch d := data.(type) {
			case record.RawData:
				if d.Schema == nil {
					return record.Record{}, cerrors.Errorf("%s: schemaless raw data not supported", transformName)
				}
				return record.Record{}, cerrors.Errorf("%s: data with schema not supported yet", transformName) // TODO
			case record.StructuredData:
				// TODO add support for nested fields
				switch d[fieldName].(type) {
				case string:
					d[fieldName] = replacement
				case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64: // any numeric type
					// ignore error, i is going to be zero if it fails anyway
					i, _ := strconv.Atoi(replacement)
					d[fieldName] = i
				default:
					fieldType := reflect.TypeOf(d[fieldName])
					zeroValue := reflect.New(fieldType).Elem().Interface()
					d[fieldName] = zeroValue
				}
			default:
				return record.Record{}, cerrors.Errorf("%s: unexpected data type %T", transformName, data)
			}

			r = getSetter.Set(r, data)
			return r, nil
		},
	}, nil
}
