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

package txfbuiltin

import (
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/transform"
	"github.com/conduitio/conduit/pkg/record"
)

const (
	extractFieldKeyName     = "extractfieldkey"
	extractFieldPayloadName = "extractfieldpayload"

	extractFieldConfigField = "field"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(extractFieldKeyName, transform.NewBuilder(ExtractFieldKey))
	processor.GlobalBuilderRegistry.MustRegister(extractFieldPayloadName, transform.NewBuilder(ExtractFieldPayload))
}

// ExtractFieldKey builds the following transform:
//  * If the key is raw and has a schema attached, extract the field and use it
//    to replace the entire key.
//  * If the key is raw and has no schema, return an error (not supported).
//  * If the key is structured, extract the field and use it to replace the
//    entire key.
func ExtractFieldKey(config transform.Config) (transform.Transform, error) {
	return extractField(extractFieldKeyName, recordKeyGetSetter{}, config)
}

// ExtractFieldPayload builds the following transformation:
//  * If the payload is raw and has a schema attached, extract the field and use
//    it to replace the entire payload.
//  * If the payload is raw and has no schema, return an error (not supported).
//  * If the payload is structured, extract the field and use it to replace the
//    entire payload.
func ExtractFieldPayload(config transform.Config) (transform.Transform, error) {
	return extractField(extractFieldPayloadName, recordPayloadGetSetter{}, config)
}

func extractField(
	transformName string,
	getSetter recordDataGetSetter,
	config transform.Config,
) (transform.Transform, error) {
	var (
		err       error
		fieldName string
	)

	if fieldName, err = getConfigFieldString(config, extractFieldConfigField); err != nil {
		return nil, cerrors.Errorf("%s: %w", transformName, err)
	}

	return func(r record.Record) (record.Record, error) {
		data := getSetter.Get(r)

		switch d := data.(type) {
		case record.RawData:
			if d.Schema == nil {
				return record.Record{}, cerrors.Errorf("%s: schemaless raw data not supported", transformName)
			}
			return record.Record{}, cerrors.Errorf("%s: data with schema not supported yet", transformName) // TODO
		case record.StructuredData:
			// TODO add support for nested fields
			extractedField := d[fieldName]
			if extractedField == nil {
				return record.Record{}, cerrors.Errorf("%s: field %q not found", transformName, fieldName)
			}

			switch v := extractedField.(type) {
			case map[string]interface{}:
				data = record.StructuredData(v)
			case []byte:
				data = record.RawData{Raw: v}
			default:
				// marshal as string by default
				data = record.RawData{Raw: []byte(fmt.Sprint(v))}
			}
		default:
			return record.Record{}, cerrors.Errorf("%s: unexpected data type %T", transformName, data)
		}

		r = getSetter.Set(r, data)
		return r, nil
	}, nil
}
