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
	"encoding/json"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
)

const (
	unwrapRecordPayloadProcType = "unwraprecord"

	// todo: support openCDC format
	unwrapRecordConfigFormat = "format" // only debezium for now
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(unwrapRecordPayloadProcType, UnwrapRecordPayload)
}

// UnwrapRecordPayload unwraps the record format from the payload and returns the record as openCDC format
func UnwrapRecordPayload(config processor.Config) (processor.Interface, error) {
	return unwrapRecord(unwrapRecordPayloadProcType, recordPayloadGetSetter{}, config)
}

func unwrapRecord(
	processorType string,
	getSetter recordDataGetSetter,
	config processor.Config,
) (processor.Interface, error) {
	var (
		err    error
		format string
		r      record.Record
	)

	format = config.Settings[unwrapRecordConfigFormat]
	if format == "" {
		return nil, cerrors.Errorf("%s: record format type not specified", processorType)
	}

	return processor.InterfaceFunc(func(_ context.Context, rec record.Record) (record.Record, error) {
		data := getSetter.Get(rec) // record.payload.after
		var jsonData record.StructuredData
		switch d := data.(type) {
		case record.RawData:
			// unmarshal raw data to structured
			err = json.Unmarshal(data.Bytes(), &jsonData)
			if err != nil {
				return record.Record{}, cerrors.Errorf("%s: %w", processorType, err)
			}
		case record.StructuredData:
			jsonData = d
		default:
			return record.Record{}, cerrors.Errorf("%s: unexpected data type %T", processorType, data)
		}

		payloadJSON, ok := jsonData[DebeziumFieldPayload]
		if !ok {
			return record.Record{}, cerrors.Errorf("%s: the payload is missing from the record", processorType)
		}

		// cast payload into a map
		payload := payloadJSON.(map[string]any)
		// validate all the debezium fields exist
		err = checkDebeziumFieldsExist(payload)
		if err != nil {
			return record.Record{}, cerrors.Errorf("%s: %w", processorType, err)
		}

		// convert json data into a record
		before := convertJSONToStructuredData(payload[DebeziumFieldBefore])
		after := convertJSONToStructuredData(payload[DebeziumFieldAfter])
		metadata := convertJSONToMetadata(payload[DebeziumFieldSource])

		r.Payload.Before = before
		r.Payload.After = after
		r.Metadata = metadata
		r.Operation = getRecordOp(payload[DebeziumFieldOp].(string))

		return r, nil
	}), nil
}

const (
	DebeziumOpCreate = "c"
	DebeziumOpUpdate = "u"
	DebeziumOpDelete = "d"
	DebeziumOpRead   = "r" // snapshot

	DebeziumFieldBefore  = "before"
	DebeziumFieldAfter   = "after"
	DebeziumFieldSource  = "source"
	DebeziumFieldOp      = "op"
	DebeziumFieldPayload = "payload"
)

type DebeziumOp string

func getRecordOp(o string) record.Operation {
	switch o {
	case DebeziumOpCreate:
		return record.OperationCreate
	case DebeziumOpUpdate:
		return record.OperationUpdate
	case DebeziumOpDelete:
		return record.OperationDelete
	case DebeziumOpRead:
		return record.OperationSnapshot
	}
	return 0 // invalid op
}

func checkDebeziumFieldsExist(data record.StructuredData) error {
	var multiErr error
	if _, ok := data[DebeziumFieldBefore]; !ok {
		multiErr = multierror.Append(multiErr, cerrors.Errorf("the %q field is missing from debezium payload", DebeziumFieldBefore))
	}
	if _, ok := data[DebeziumFieldAfter]; !ok {
		multiErr = multierror.Append(multiErr, cerrors.Errorf("the %q field is missing from debezium payload", DebeziumFieldAfter))
	}
	if _, ok := data[DebeziumFieldSource]; !ok {
		multiErr = multierror.Append(multiErr, cerrors.Errorf("the %q field is missing from debezium payload", DebeziumFieldSource))
	}
	if _, ok := data[DebeziumFieldOp]; !ok {
		multiErr = multierror.Append(multiErr, cerrors.Errorf("the %q field is missing from debezium payload", DebeziumFieldOp))
	}
	// ts_ms and transaction can be empty
	return multiErr
}

func convertJSONToStructuredData(mp any) record.StructuredData {
	if mp == nil {
		return nil
	}

	structured := record.StructuredData{}
	for k, v := range mp.(map[string]any) {
		structured[k] = v
	}
	return structured
}

func convertJSONToMetadata(mp any) record.Metadata {
	if mp == nil {
		return nil
	}

	meta := record.Metadata{}
	for k, v := range mp.(map[string]interface{}) {
		meta[k] = v.(string)
	}
	return meta
}
