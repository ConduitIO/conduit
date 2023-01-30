// Copyright © 2023 Meroxa, Inc.
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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
)

type unwrapProcessor struct {
	unwrapper unwrapper
}

// unwrapper unwraps the formatted record from the openCDC record
type unwrapper interface {
	// Unwrap gets the unwrapped record
	Unwrap(record.Record) (record.Record, error)
}

const (
	unwrapPayloadProcType = "unwrap"

	unwrapConfigFormat = "format"

	FormatDebezium     = "debezium"
	FormatKafkaConnect = "kafka-connect"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(unwrapPayloadProcType, UnwrapBuilder)
}

func UnwrapBuilder(config processor.Config) (processor.Interface, error) {
	if _, ok := config.Settings[unwrapConfigFormat]; !ok {
		return nil, cerrors.Errorf("%s: %q config not specified", unwrapPayloadProcType, unwrapConfigFormat)
	}
	format := config.Settings[unwrapConfigFormat]
	proc := &unwrapProcessor{}
	switch format {
	case FormatDebezium:
		proc.unwrapper = &DebeziumUnwrapper{}
	case FormatKafkaConnect:
		proc.unwrapper = &KafkaConnectUnwrapper{}
	default:
		return nil, cerrors.Errorf("%s: %q is not a valid format", unwrapPayloadProcType, format)
	}

	return NewFuncWrapper(proc.Process), nil
}

func (p *unwrapProcessor) Process(ctx context.Context, in record.Record) (record.Record, error) {
	data := in.Payload.After
	var structData record.StructuredData
	switch d := data.(type) {
	case record.RawData:
		// todo: take this section out, after platform team support ordering processors
		// unmarshal raw data to structured
		err := json.Unmarshal(data.Bytes(), &structData)
		if err != nil {
			return record.Record{}, cerrors.Errorf("failed to unmarshal raw data as JSON: %w", unwrapPayloadProcType, err)
		}
	case record.StructuredData:
		structData = d
	default:
		return record.Record{}, cerrors.Errorf("unexpected data type %T", unwrapPayloadProcType, data)
	}
	// assign the structured data to payload.After
	in.Payload.After = structData

	out, err := p.unwrapper.Unwrap(in)
	if err != nil {
		return record.Record{}, cerrors.Errorf("%s: error unwrapping record: %w", unwrapPayloadProcType, err)
	}
	return out, nil
}

/*
	 example of kafka-connect record:
		`{
		 "payload": {
		     "description": "desc",
		     "id": 20
		   },
		 "schema": {} // will be ignored
		}`
*/
// KafkaConnectUnwrapper unwraps a kafka connect record from the payload, expects rec.Payload.After to be of type record.StructuredData
type KafkaConnectUnwrapper struct{}

func (kp *KafkaConnectUnwrapper) Unwrap(rec record.Record) (record.Record, error) {
	// record must be structured
	structPayload, ok := rec.Payload.After.(record.StructuredData)
	if !ok {
		return record.Record{}, cerrors.Errorf("record payload data must be structured data")
	}

	// get payload
	payload := structPayload["payload"]
	if p, ok := payload.(map[string]interface{}); ok {
		structPayload = p
	} else {
		return record.Record{}, cerrors.Errorf("payload doesn't contain a record")
	}

	return record.Record{
		Key:      rec.Key,
		Position: rec.Position,
		Metadata: nil,
		Payload: record.Change{
			Before: nil,
			After:  structPayload,
		},
		Operation: record.OperationSnapshot,
	}, nil
}

/*
	 example of debezium record:
		`{
		 "payload": {
		   "after": {
		     "description": "desc",
		     "id": 20
		   },
		   "before": null,
		   "op": "c",
		   "source": {
		     "opencdc.readAt": "1674061777225877000",
		     "opencdc.version": "v1",
		   },
		   "transaction": null,
		   "ts_ms": 1674061777225
		 },
		 "schema": {} // will be ignored
		}`
*/
// DebeziumUnwrapper unwraps a debezium record from the payload, expects rec.Payload.After to be of type record.StructuredData
type DebeziumUnwrapper struct{}

const (
	debeziumOpCreate = "c"
	debeziumOpUpdate = "u"
	debeziumOpDelete = "d"
	debeziumOpRead   = "r" // snapshot

	debeziumFieldBefore = "before"
	debeziumFieldAfter  = "after"
	debeziumFieldSource = "source"
	debeziumFieldOp     = "op"
	debeziumFieldTsMs   = "ts_ms"
)

func (d *DebeziumUnwrapper) Unwrap(rec record.Record) (record.Record, error) {
	// record must be structured
	structPayload, ok := rec.Payload.After.(record.StructuredData)
	if !ok {
		return record.Record{}, cerrors.Errorf("record payload data must be structured data")
	}
	// get payload
	payload := structPayload["payload"] // the payload has the debezium record
	if p, ok := payload.(map[string]interface{}); ok {
		structPayload = p
	} else {
		return record.Record{}, cerrors.Errorf("payload doesn't contain a record")
	}

	// check fields under payload
	err := d.validateRecord(structPayload)
	if err != nil {
		return record.Record{}, err
	}

	var before record.StructuredData
	beforeData := structPayload[debeziumFieldBefore]
	if b, ok := beforeData.(map[string]any); beforeData != nil && !ok {
		return record.Record{}, cerrors.Errorf("%s field is not a map", debeziumFieldBefore)
	} else {
		before = b
	}

	var after record.StructuredData
	afterData := structPayload[debeziumFieldAfter]
	if a, ok := afterData.(map[string]any); afterData != nil && !ok {
		return record.Record{}, cerrors.Errorf("%s field is not a map", debeziumFieldAfter)
	} else {
		after = a
	}

	op, ok := structPayload[debeziumFieldOp].(string)
	if !ok {
		return record.Record{}, cerrors.Errorf("%s operation is not a string", op)
	}

	operation, err := d.convertOperation(op)
	if err != nil {
		return record.Record{}, cerrors.Errorf("error unwrapping operation: %w", err)
	}

	metadata, err := d.unwrapMetadata(rec)
	if err != nil {
		return record.Record{}, cerrors.Errorf("error unwrapping metadata: %w", err)
	}

	return record.Record{
		Key:       rec.Key,
		Position:  rec.Position,
		Operation: operation,
		Payload: record.Change{
			Before: before,
			After:  after,
		},
		Metadata: metadata,
	}, nil
}

func (d *DebeziumUnwrapper) validateRecord(data record.StructuredData) error {
	var multiErr error
	if _, ok := data[debeziumFieldBefore]; !ok {
		multiErr = multierror.Append(multiErr, cerrors.Errorf("the %q field is missing from debezium payload", debeziumFieldBefore))
	}
	if _, ok := data[debeziumFieldAfter]; !ok {
		multiErr = multierror.Append(multiErr, cerrors.Errorf("the %q field is missing from debezium payload", debeziumFieldAfter))
	}
	if _, ok := data[debeziumFieldSource]; !ok {
		multiErr = multierror.Append(multiErr, cerrors.Errorf("the %q field is missing from debezium payload", debeziumFieldSource))
	}
	if _, ok := data[debeziumFieldOp]; !ok {
		multiErr = multierror.Append(multiErr, cerrors.Errorf("the %q field is missing from debezium payload", debeziumFieldOp))
	}
	// ts_ms and transaction can be empty
	return multiErr
}

func (d *DebeziumUnwrapper) unwrapMetadata(rec record.Record) (record.Metadata, error) {
	meta := record.Metadata{}
	// add original record's metadata to meta
	for k, v := range rec.Metadata {
		meta[k] = v
	}

	var structPayload record.StructuredData
	if p, ok := rec.Payload.After.(record.StructuredData)["payload"].(map[string]interface{}); ok {
		structPayload = p
	} else {
		return nil, cerrors.Errorf("payload doesn't contain a record")
	}

	// set metadata readAt time
	var t time.Time
	tsMs := structPayload[debeziumFieldTsMs]
	if tsMs != nil {
		floatTime, ok := tsMs.(float64)
		if !ok {
			return nil, cerrors.Errorf("%s is not a float", debeziumFieldTsMs)
		}
		t = time.Unix(0, int64(floatTime)*int64(time.Millisecond))
	}
	meta.SetReadAt(t)

	// return current metadata if "source" field doesn't exist
	if structPayload[debeziumFieldSource] == nil {
		return meta, nil
	}

	mp, ok := structPayload[debeziumFieldSource].(map[string]interface{})
	if !ok {
		return nil, cerrors.Errorf("%q is not formatted as a map", debeziumFieldSource)
	}

	for k, v := range mp {
		if str, ok := v.(string); ok {
			meta[k] = str
		} else {
			return nil, cerrors.Errorf("the value %q from the field %q is not a string", v, debeziumFieldSource)
		}
	}
	return meta, nil
}

// convertOperation converts debezium operation to openCDC operation
func (d *DebeziumUnwrapper) convertOperation(op string) (record.Operation, error) {
	switch op {
	case debeziumOpCreate:
		return record.OperationCreate, nil
	case debeziumOpUpdate:
		return record.OperationUpdate, nil
	case debeziumOpDelete:
		return record.OperationDelete, nil
	case debeziumOpRead:
		return record.OperationSnapshot, nil
	}
	return 0, cerrors.Errorf("%q is an invalid operation", op)
}
