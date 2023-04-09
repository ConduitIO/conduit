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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
)

const (
	timestampConvertorKeyProcType     = "timestampconvertorkey"
	timestampConvertorPayloadProcType = "timestampconvertorpayload"

	timestampConvertorConfigTargetType = "target.type"
	timestampConvertorConfigField      = "date"
	timestampConvertorConfigFormat     = "format"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(timestampConvertorKeyProcType, TimestampConvertorKey)
	processor.GlobalBuilderRegistry.MustRegister(timestampConvertorPayloadProcType, TimestampConvertorPayload)
}

// TimestampConvertorKey builds a processor which converts a timestamp in a field in the key
// into a different type. The supported types are:
//   - "string"
//   - "unix"
//   - "time.Time".
//
// Any combination of the supported types is possible. For example, it's possible
// to convert from a Unix timestamp to Go's time.Time or to convert from a string
// to a Unix timestamp.
//
// The processor supports only structured data.
func TimestampConvertorKey(config processor.Config) (processor.Interface, error) {
	return timestampConvertor(timestampConvertorKeyProcType, recordKeyGetSetter{}, config)
}

// TimestampConvertorPayload builds the same processor as TimestampConvertorKey, except that
// it operates on the field Record.Payload.After.
func TimestampConvertorPayload(config processor.Config) (processor.Interface, error) {
	return timestampConvertor(timestampConvertorPayloadProcType, recordPayloadGetSetter{}, config)
}

func timestampConvertor(
	processorType string,
	getSetter recordDataGetSetter,
	config processor.Config,
) (processor.Interface, error) {
	const (
		stringType = "string"
		unixType   = "unix"
		timeType   = "time.Time"
	)

	var (
		err        error
		targetType string
		field      string
		format     string
	)

	// if field is empty then input is raw data
	if field, err = getConfigFieldString(config, timestampConvertorConfigField); err != nil {
		return nil, cerrors.Errorf("%s: %w", processorType, err)
	}
	if targetType, err = getConfigFieldString(config, timestampConvertorConfigTargetType); err != nil {
		return nil, cerrors.Errorf("%s: %w", processorType, err)
	}
	if targetType != stringType && targetType != unixType && targetType != timeType {
		return nil, cerrors.Errorf("%s: targetType (%s) is not supported", processorType, targetType)
	}
	format = config.Settings[timestampConvertorConfigFormat] // can be empty
	if format == "" && targetType == stringType {
		return nil, cerrors.Errorf("%s: format is needed to parse the output", processorType)
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
			var tm time.Time
			switch v := d[field].(type) {
			case int64:
				tm = time.Unix(0, v).UTC()
			case string:
				if format == "" {
					return record.Record{}, cerrors.Errorf("%s: no format to parse the date", processorType)
				}
				tm, err = time.Parse(format, v)
				if err != nil {
					return record.Record{}, cerrors.Errorf("%s: %w", processorType, err)
				}
			case time.Time:
				tm = v
			default:
				return record.Record{}, cerrors.Errorf("%s: unexpected data type %T", processorType, d[field])
			}
			// TODO add support for nested fields
			switch targetType {
			case stringType: // use "format" to generate the output
				d[field] = tm.Format(format)
			case unixType:
				d[field] = tm.UnixNano()
			case timeType:
				d[field] = tm
			default:
				return record.Record{}, cerrors.Errorf("%s: unexpected output type %T", processorType, targetType)
			}
		default:
			return record.Record{}, cerrors.Errorf("%s: unexpected data type %T", processorType, data)
		}

		r = getSetter.Set(r, data)
		return r, nil
	}), nil
}
