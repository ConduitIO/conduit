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
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
)

const (
	replaceFieldKeyProcType     = "replacefieldkey"
	replaceFieldPayloadProcType = "replacefieldpayload"

	replaceFieldConfigExclude = "exclude"
	replaceFieldConfigInclude = "include"
	replaceFieldConfigRename  = "rename"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(replaceFieldKeyProcType, ReplaceFieldKey)
	processor.GlobalBuilderRegistry.MustRegister(replaceFieldPayloadProcType, ReplaceFieldKey)
}

// ReplaceFieldKey builds a processor which replaces a field in the key in raw
// data with a schema or in structured data. Raw data without a schema is not
// supported. The processor can be controlled by 3 variables:
//  * "exclude" - is a comma separated list of fields that should be excluded
//    from the processed record ("exclude" takes precedence over "include").
//  * "include" - is a comma separated list of fields that should be included
//    in the processed record.
//  * "rename" - is a comma separated list of pairs separated by colons, that
//    controls the mapping of old field names to new field names.
// If "include" is not configured or is empty then all fields in the record will
// be included by default (except if they are configured in "exclude").
// If "include" is not empty, then all fields are excluded by default and only
// fields in "include" will be added to the processed record.
func ReplaceFieldKey(config processor.Config) (processor.Interface, error) {
	return replaceField(replaceFieldKeyProcType, recordKeyGetSetter{}, config)
}

// ReplaceFieldPayload builds the same processor as ReplaceFieldKey, except that
// it operates on the field Record.Payload.After.
func ReplaceFieldPayload(config processor.Config) (processor.Interface, error) {
	return replaceField(replaceFieldPayloadProcType, recordPayloadGetSetter{}, config)
}

func replaceField(
	processorType string,
	getSetter recordDataGetSetter,
	config processor.Config,
) (processor.Interface, error) {
	var (
		exclude string
		include string
		rename  string

		excludeMap = make(map[string]bool)
		includeMap = make(map[string]bool)
		renameMap  = make(map[string]string)
	)

	exclude = config.Settings[replaceFieldConfigExclude]
	include = config.Settings[replaceFieldConfigInclude]
	rename = config.Settings[replaceFieldConfigRename]

	if exclude == "" && include == "" && rename == "" {
		return nil, cerrors.Errorf(
			"%s: config must include at least one of [%s %s %s]",
			processorType,
			replaceFieldConfigExclude,
			replaceFieldConfigInclude,
			replaceFieldConfigRename,
		)
	}

	if rename != "" {
		pairs := strings.Split(rename, ",")
		for _, pair := range pairs {
			tokens := strings.Split(pair, ":")
			if len(tokens) != 2 {
				return nil, cerrors.Errorf(
					"%s: config field %q contains invalid value %q, expected format is \"foo:c1,bar:c2\"",
					processorType,
					replaceFieldConfigRename,
					rename,
				)
			}
			renameMap[tokens[0]] = tokens[1]
		}
	}
	if exclude != "" {
		excludeList := strings.Split(exclude, ",")
		for _, v := range excludeList {
			excludeMap[v] = true
		}
	}
	if include != "" {
		includeList := strings.Split(include, ",")
		for _, v := range includeList {
			includeMap[v] = true
		}
	}

	return processor.InterfaceFunc(func(_ context.Context, r record.Record) (record.Record, error) {
		data := getSetter.Get(r)

		switch d := data.(type) {
		case record.RawData:
			if d.Schema == nil {
				return record.Record{}, cerrors.Errorf("%s: schemaless raw data not supported", processorType)
			}
			return record.Record{}, cerrors.Errorf("%s: data with schema not supported yet", processorType) // TODO
		case record.StructuredData:
			// TODO add support for nested fields
			for field, value := range d {
				if excludeMap[field] || (len(includeMap) != 0 && !includeMap[field]) {
					delete(d, field)
					continue
				}
				if newField, ok := renameMap[field]; ok {
					delete(d, field)
					d[newField] = value
				}
			}
		default:
			return record.Record{}, cerrors.Errorf("%s: unexpected data type %T", processorType, data)
		}

		r = getSetter.Set(r, data)
		return r, nil
	}), nil
}
