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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/inspector"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/rs/zerolog"
)

type unwrapProcessor struct {
	unWrapper unWrapper

	inInsp  *inspector.Inspector
	outInsp *inspector.Inspector
}

// unWrapper unwraps the formatted record from the openCDC record
type unWrapper interface {
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
	if format != FormatDebezium && format != FormatKafkaConnect {
		return nil, cerrors.Errorf("%s: %q is not a valid format", unwrapPayloadProcType, format)
	}

	cw := zerolog.NewConsoleWriter()
	cw.TimeFormat = "2006-01-02T15:04:05+00:00"
	logger := zerolog.New(cw).With().Timestamp().Logger()
	proc := &unwrapProcessor{
		inInsp:  inspector.New(log.New(logger), inspector.DefaultBufferSize),
		outInsp: inspector.New(log.New(logger), inspector.DefaultBufferSize),
	}
	switch format {
	case FormatDebezium:
		proc.unWrapper = &DebeziumUnWrapper{}
	case FormatKafkaConnect:
		proc.unWrapper = &KafkaConnectUnWrapper{}
	}
	return proc, nil
}

func (p *unwrapProcessor) Process(ctx context.Context, in record.Record) (record.Record, error) {
	p.inInsp.Send(ctx, in)
	out, err := p.unWrapper.Unwrap(in)
	if err != nil {
		return record.Record{}, cerrors.Errorf("%s: error unwrapping record: %w", unwrapPayloadProcType, err)
	}
	p.outInsp.Send(ctx, out)
	return out, nil
}

func (p *unwrapProcessor) InspectIn(ctx context.Context) *inspector.Session {
	return p.inInsp.NewSession(ctx)
}

func (p *unwrapProcessor) InspectOut(ctx context.Context) *inspector.Session {
	return p.outInsp.NewSession(ctx)
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
type KafkaConnectUnWrapper struct{}

func (kp *KafkaConnectUnWrapper) Unwrap(rec record.Record) (record.Record, error) {
	data := rec.Payload.After
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

	// get payload
	var structPayload record.StructuredData
	payload := structData["payload"]
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
type DebeziumUnWrapper struct{}

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

func (d *DebeziumUnWrapper) Unwrap(rec record.Record) (record.Record, error) {
	data := rec.Payload.After
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

	// get payload
	var structPayload record.StructuredData
	payload := structData["payload"] // the payload has the debezium record
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
	if b, ok := structPayload[DebeziumFieldBefore].(map[string]any); ok {
		before = b
	}

	var after record.StructuredData
	if a, ok := structPayload[DebeziumFieldAfter].(map[string]any); ok {
		after = a
	}

	var op string
	if str, ok := structPayload[DebeziumFieldOp].(string); ok {
		op = str
	} else {
		return record.Record{}, cerrors.Errorf("%s operation is not a string", op)
	}

	operation, err := d.convertOperation(op)
	if err != nil {
		return record.Record{}, cerrors.Errorf("error unwrapping operation: %w", err)
	}

	metadata, err := d.unwrapMetadata(rec, structPayload)
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

func (d *DebeziumUnWrapper) validateRecord(data record.StructuredData) error {
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

func (d *DebeziumUnWrapper) unwrapMetadata(rec record.Record, payload record.StructuredData) (record.Metadata, error) {
	if payload[DebeziumFieldSource] == nil {
		return nil, nil
	}
	meta := record.Metadata{}
	mp, ok := payload[DebeziumFieldSource].(map[string]interface{})
	if !ok {
		return nil, cerrors.Errorf("%q is not formatted as a map", DebeziumFieldSource)
	}
	for k, v := range mp {
		if str, ok := v.(string); ok {
			meta[k] = str
		} else {
			return nil, cerrors.Errorf("the value %q from the field %q is not a string", v, DebeziumFieldSource)
		}
	}
	// append original record's metadata to meta
	for k, v := range rec.Metadata {
		meta[k] = v
	}

	return meta, nil
}

// convertOperation converts debezium operation to openCDC operation
func (d *DebeziumUnWrapper) convertOperation(op string) (record.Operation, error) {
	switch op {
	case DebeziumOpCreate:
		return record.OperationCreate, nil
	case DebeziumOpUpdate:
		return record.OperationUpdate, nil
	case DebeziumOpDelete:
		return record.OperationDelete, nil
	case DebeziumOpRead:
		return record.OperationSnapshot, nil
	}
	return 0, cerrors.Errorf("%q is an invalid operation", op)
}
