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
	"encoding/base64"
	"fmt"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/goccy/go-json"
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
	FormatOpenCDC      = "opencdc"
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
	case FormatOpenCDC:
		proc.unwrapper = &openCDCUnwrapper{}
	default:
		return nil, cerrors.Errorf("%s: %q is not a valid format", unwrapProcType, format)
	}

	return NewFuncWrapper(proc.Process), nil
}

func (p *unwrapProcessor) Process(_ context.Context, in record.Record) (record.Record, error) {
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
Example of an OpenCDC record:
{
  "key": "NWQ0N2UwZGQtNTkxYi00MGEyLTk3YzMtYzc1MDY0MWU3NTc1",
  "metadata": {
    "conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
    "opencdc.readAt": "1706028881541916000",
    "opencdc.version": "v1"
  },
  "operation": "create",
  "payload": {
    "after": {
      "event_id": 2041181862,
      "msg": "string 4c88f20f-aa77-4f4b-9354-e4fdb1989a52",
      "pg_generator": false,
      "sensor_id": 54434691,
      "triggered": false
    },
    "before": null
  },
  "position": "ZWIwNmJiMmMtNWNhMS00YjUyLWE2ZmMtYzc0OTFlZDQ3OTYz"
}
*/

// openCDCUnwrapper unwraps an OpenCDC record from the payload, by unmarhsalling rec.Payload.After into type Record.
type openCDCUnwrapper struct{}

// UnwrapOperation extracts operation from a structuredData record.
func (o *openCDCUnwrapper) UnwrapOperation(structData record.StructuredData) (record.Operation, error) {
	var operation record.Operation
	op, ok := structData["operation"]
	if !ok {
		return operation, cerrors.Errorf("record payload after doesn't contain operation")
	}

	switch opType := op.(type) {
	case record.Operation:
		operation = opType
	case string:
		if err := operation.UnmarshalText([]byte(opType)); err != nil {
			return operation, cerrors.Errorf("couldn't unmarshal record operation")
		}
	default:
		return operation, cerrors.Errorf("expected a record.Operation or a string, got %T", opType)
	}
	return operation, nil
}

// UnwrapMetadata extracts metadata from a structuredData record.
func (o *openCDCUnwrapper) UnwrapMetadata(structData record.StructuredData) (record.Metadata, error) {
	var metadata record.Metadata
	meta, ok := structData["metadata"]
	if !ok {
		return metadata, cerrors.Errorf("record payload after doesn't contain metadata")
	}

	switch m := meta.(type) {
	case record.Metadata:
		metadata = m
	case map[string]interface{}:
		metadata = make(record.Metadata, len(m))
		for k, v := range m {
			metadata[k] = fmt.Sprint(v)
		}
	default:
		return metadata, cerrors.Errorf("expected a record.Metadata or a map[string]interface{}, got %T", m)
	}
	return metadata, nil
}

// UnwrapKey extracts key from a structuredData record.
func (o *openCDCUnwrapper) UnwrapKey(structData record.StructuredData) (record.Data, error) {
	var key record.Data
	ky, ok := structData["key"]
	if !ok {
		return key, cerrors.Errorf("record payload after doesn't contain key")
	}
	switch k := ky.(type) {
	case map[string]interface{}:
		convertedData := make(record.StructuredData, len(k))
		for kk, v := range k {
			convertedData[kk] = v
		}
		key = convertedData
	case string:
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(k)))
		n, err := base64.StdEncoding.Decode(decoded, []byte(k))
		if err != nil {
			return key, cerrors.Errorf("couldn't decode key: %w", err)
		}
		key = record.RawData{Raw: decoded[:n]}
	default:
		return key, cerrors.Errorf("expected a record.Data or a string, got %T", k)
	}
	return key, nil
}

func (o *openCDCUnwrapper) convertPayloadData(payload map[string]interface{}, key string) (record.Data, error) {
	payloadData, ok := payload[key]
	if !ok {
		return nil, nil
	}

	switch data := payloadData.(type) {
	case map[string]interface{}:
		convertedData := make(record.StructuredData, len(data))
		for k, v := range data {
			convertedData[k] = v
		}
		return convertedData, nil
	case string:
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(data)))
		n, err := base64.StdEncoding.Decode(decoded, []byte(data))
		if err != nil {
			return nil, cerrors.Errorf("couldn't decode payload %s: %w", err, key)
		}
		return record.RawData{Raw: decoded[:n]}, nil
	default:
		return nil, nil
	}
}

// UnwrapPayload extracts payload from a structuredData record.
func (o *openCDCUnwrapper) UnwrapPayload(structData record.StructuredData) (record.Change, error) {
	var payload record.Change
	pl, ok := structData["payload"]
	if !ok {
		return payload, cerrors.Errorf("record payload doesn't contain payload")
	}

	switch p := pl.(type) {
	case record.Change:
		payload = p
	case map[string]interface{}:
		before, err := o.convertPayloadData(p, "before")
		if err != nil {
			return record.Change{}, err
		}

		after, err := o.convertPayloadData(p, "after")
		if err != nil {
			return record.Change{}, err
		}

		payload = record.Change{
			Before: before,
			After:  after,
		}
	default:
		return record.Change{}, cerrors.Errorf("expected a record.Change or a map[string]interface{}, got %T", p)
	}
	return payload, nil
}

// Unwrap replaces the whole record.payload with record.payload.after.payload except position.
func (o *openCDCUnwrapper) Unwrap(rec record.Record) (record.Record, error) {
	var structData record.StructuredData
	data := rec.Payload.After
	switch d := data.(type) {
	case record.RawData:
		// unmarshal raw data to structured
		if err := json.Unmarshal(data.Bytes(), &structData); err != nil {
			return record.Record{}, cerrors.Errorf("failed to unmarshal raw data as JSON: %w", unwrapProcType, err)
		}
	case record.StructuredData:
		structData = d
	default:
		return record.Record{}, cerrors.Errorf("unexpected data type %T", unwrapProcType, data)
	}

	operation, err := o.UnwrapOperation(structData)
	if err != nil {
		return record.Record{}, err
	}

	metadata, err := o.UnwrapMetadata(structData)
	if err != nil {
		return record.Record{}, err
	}

	key, err := o.UnwrapKey(structData)
	if err != nil {
		return record.Record{}, err
	}

	payload, err := o.UnwrapPayload(structData)
	if err != nil {
		return record.Record{}, err
	}

	// Position is the only key we preserve from the original record to maintain the reference respect other messages
	// that will be coming from in the event of chaining pipelines (e.g.: source -> kafka, kafka -> destination)
	return record.Record{
		Key:       key,
		Position:  rec.Position,
		Metadata:  metadata,
		Payload:   payload,
		Operation: operation,
	}, nil
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
	structPayload, ok = structPayload["payload"].(map[string]any)
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
	debeziumRec, ok = debeziumRec["payload"].(map[string]any) // the payload has the debezium record
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
	debeziumRec := rec.Payload.After.(record.StructuredData)["payload"].(map[string]any)

	var source map[string]string
	for field, val := range debeziumRec {
		switch field {
		case debeziumFieldAfter, debeziumFieldBefore, debeziumFieldOp:
			continue // ignore
		case debeziumFieldTimestamp:
			tsMs, ok := val.(float64)
			if !ok {
				return nil, cerrors.Errorf("%s is not a float", debeziumFieldTimestamp)
			}
			readAt := time.UnixMilli(int64(tsMs))
			rec.Metadata.SetReadAt(readAt)
		case debeziumFieldSource:
			// don't add prefix for source fields to be consistent with the
			// behavior of the debezium converter in the SDK - it puts all
			// metadata fields into the `source` field
			source = d.flatten("", val)
		default:
			flattened := d.flatten("debezium."+field, val)
			for k, v := range flattened {
				rec.Metadata[k] = v
			}
		}
	}

	// source is added at the end to overwrite any other fields
	for k, v := range source {
		rec.Metadata[k] = v
	}

	return rec.Metadata, nil
}

func (d *debeziumUnwrapper) flatten(key string, val any) map[string]string {
	var prefix string
	if len(key) > 0 {
		prefix = key + "."
	}
	switch val := val.(type) {
	case map[string]any:
		out := make(map[string]string)
		for k1, v1 := range val {
			for k2, v2 := range d.flatten(prefix+k1, v1) {
				out[k2] = v2
			}
		}
		return out
	case nil:
		return nil
	case string:
		return map[string]string{key: val}
	default:
		return map[string]string{key: fmt.Sprint(val)}
	}
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
