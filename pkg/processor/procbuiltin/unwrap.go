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
	"fmt"
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
	unwrapProcType = "unwrap"

	unwrapConfigFormat = "format"

	FormatDebezium     = "debezium"
	FormatKafkaConnect = "kafka-connect"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(unwrapProcType, Unwrap)
}

func Unwrap(config processor.Config) (processor.Interface, error) {
	if _, ok := config.Settings[unwrapConfigFormat]; !ok {
		return nil, cerrors.Errorf("%s: %q config not specified", unwrapProcType, unwrapConfigFormat)
	}
	format := config.Settings[unwrapConfigFormat]
	proc := &unwrapProcessor{}
	switch format {
	case FormatDebezium:
		proc.unwrapper = &debeziumUnwrapper{}
	case FormatKafkaConnect:
		proc.unwrapper = &kafkaConnectUnwrapper{}
	default:
		return nil, cerrors.Errorf("%s: %q is not a valid format", unwrapProcType, format)
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
			return record.Record{}, cerrors.Errorf("failed to unmarshal raw data as JSON: %w", unwrapProcType, err)
		}
	case record.StructuredData:
		structData = d
	default:
		return record.Record{}, cerrors.Errorf("unexpected data type %T", unwrapProcType, data)
	}
	// assign the structured data to payload.After
	in.Payload.After = structData

	out, err := p.unwrapper.Unwrap(in)
	if err != nil {
		return record.Record{}, cerrors.Errorf("%s: error unwrapping record: %w", unwrapProcType, err)
	}
	return out, nil
}

/*
Example of a kafka-connect record:
  {
    "payload": {
    "description": "desc",
      "id": 20
    },
    "schema": {} // will be ignored
  }
*/

// kafkaConnectUnwrapper unwraps a kafka connect record from the payload, expects rec.Payload.After to be of type record.StructuredData
type kafkaConnectUnwrapper struct{}

func (k *kafkaConnectUnwrapper) Unwrap(rec record.Record) (record.Record, error) {
	// record must be structured
	structPayload, ok := rec.Payload.After.(record.StructuredData)
	if !ok {
		return record.Record{}, cerrors.Errorf("record payload data must be structured data")
	}

	// get payload
	structPayload, ok = structPayload["payload"].(map[string]interface{})
	if !ok {
		return record.Record{}, cerrors.Errorf("payload doesn't contain a record")
	}

	return record.Record{
		Key:      k.UnwrapKey(rec.Key),
		Position: rec.Position,
		Metadata: nil,
		Payload: record.Change{
			Before: nil,
			After:  structPayload,
		},
		Operation: record.OperationSnapshot,
	}, nil
}

// UnwrapKey unwraps key as a kafka connect formatted record, returns the key's payload content, or returns the
// original key if payload doesn't exist.
func (k *kafkaConnectUnwrapper) UnwrapKey(key record.Data) record.Data {
	// convert the key to structured data
	var structKey record.StructuredData
	switch d := key.(type) {
	case record.RawData:
		// try unmarshalling raw key
		err := json.Unmarshal(key.Bytes(), &structKey)
		// if key is not json formatted, return the original key
		if err != nil {
			return key
		}
	case record.StructuredData:
		structKey = d
	}

	payload, ok := structKey["payload"]
	// return the original key if it doesn't contain a payload
	if !ok {
		return key
	}

	// if payload is a map, return the payload as structured data
	if p, ok := payload.(map[string]any); ok {
		return record.StructuredData(p)
	}

	// otherwise, convert the payload to string, then return it as raw data
	raw := fmt.Sprint(payload)

	return record.RawData{Raw: []byte(raw)}
}

/*
Example of a debezium record:
  {
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
  }
*/

// debeziumUnwrapper unwraps a debezium record from the payload.
type debeziumUnwrapper struct {
	kafkaConnectUnwrapper kafkaConnectUnwrapper
}

const (
	debeziumOpCreate = "c"
	debeziumOpUpdate = "u"
	debeziumOpDelete = "d"
	debeziumOpRead   = "r"      // snapshot
	debeziumOpUnset  = "$unset" // mongoDB unset operation

	debeziumFieldBefore    = "before"
	debeziumFieldAfter     = "after"
	debeziumFieldSource    = "source"
	debeziumFieldOp        = "op"
	debeziumFieldTimestamp = "ts_ms"
)

func (d *debeziumUnwrapper) Unwrap(rec record.Record) (record.Record, error) {
	// record must be structured
	debeziumRec, ok := rec.Payload.After.(record.StructuredData)
	if !ok {
		return record.Record{}, cerrors.Errorf("record payload data must be structured data")
	}
	// get payload
	debeziumRec, ok = debeziumRec["payload"].(map[string]interface{}) // the payload has the debezium record
	if !ok {
		return record.Record{}, cerrors.Errorf("payload doesn't contain a record")
	}

	// check fields under payload
	err := d.validateRecord(debeziumRec)
	if err != nil {
		return record.Record{}, err
	}

	before, err := d.valueToData(debeziumRec[debeziumFieldBefore])
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to parse field %s: %w", debeziumFieldBefore, err)
	}

	after, err := d.valueToData(debeziumRec[debeziumFieldAfter])
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to parse field %s: %w", debeziumFieldAfter, err)
	}

	op, ok := debeziumRec[debeziumFieldOp].(string)
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
		Key:       d.kafkaConnectUnwrapper.UnwrapKey(rec.Key),
		Position:  rec.Position,
		Operation: operation,
		Payload: record.Change{
			Before: before,
			After:  after,
		},
		Metadata: metadata,
	}, nil
}

func (d *debeziumUnwrapper) valueToData(val any) (record.Data, error) {
	switch v := val.(type) {
	case map[string]any:
		return record.StructuredData(v), nil
	case string:
		return record.RawData{Raw: []byte(v)}, nil
	case nil:
		// nil is allowed
		return nil, nil
	default:
		return nil, cerrors.Errorf("expected a map or a string, got %T", val)
	}
}

func (d *debeziumUnwrapper) validateRecord(data record.StructuredData) error {
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

func (d *debeziumUnwrapper) unwrapMetadata(rec record.Record) (record.Metadata, error) {
	meta := record.Metadata{}
	// add original record's metadata to meta
	for k, v := range rec.Metadata {
		meta[k] = v
	}

	structPayload := rec.Payload.After.(record.StructuredData)["payload"].(map[string]interface{})

	// set metadata readAt time
	var t time.Time
	tsMs := structPayload[debeziumFieldTimestamp]
	if tsMs != nil {
		floatTime, ok := tsMs.(float64)
		if !ok {
			return nil, cerrors.Errorf("%s is not a float", debeziumFieldTimestamp)
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
func (d *debeziumUnwrapper) convertOperation(op string) (record.Operation, error) {
	switch op {
	case debeziumOpCreate:
		return record.OperationCreate, nil
	case debeziumOpUpdate:
		return record.OperationUpdate, nil
	case debeziumOpDelete:
		return record.OperationDelete, nil
	case debeziumOpRead:
		return record.OperationSnapshot, nil
	case debeziumOpUnset:
		return record.OperationUpdate, nil
	}
	return 0, cerrors.Errorf("%q is an invalid operation", op)
}
