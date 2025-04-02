// Copyright © 2024 Meroxa, Inc.
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

//go:generate paramgen -output=debezium_paramgen.go debeziumConfig

package unwrap

import (
	"context"
	"fmt"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
)

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

type debeziumConfig struct {
	// Field is a reference to the field that contains the Debezium record.
	//
	// For more information about the format, see [Referencing fields](https://conduit.io/docs/using/processors/referencing-fields).
	Field string `json:"field" validate:"regex=^.Payload" default:".Payload.After"`
}

type DebeziumProcessor struct {
	sdk.UnimplementedProcessor

	logger      log.CtxLogger
	fieldRefRes sdk.ReferenceResolver
}

func NewDebeziumProcessor(logger log.CtxLogger) *DebeziumProcessor {
	return &DebeziumProcessor{logger: logger}
}

func (d *DebeziumProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "unwrap.debezium",
		Summary: "Unwraps a Debezium record from the input [OpenCDC record](https://conduit.io/docs/using/opencdc-record).",
		Description: `In this processor, the wrapped (Debezium) record replaces the wrapping record (being processed) 
completely, except for the position.

The Debezium record's metadata and the wrapping record's metadata is merged, with the Debezium metadata having precedence.

This is useful in cases where Conduit acts as an intermediary between a Debezium source and a Debezium destination. 
In such cases, the Debezium record is set as the [OpenCDC record](https://conduit.io/docs/using/opencdc-record)'s payload,` +
			`and needs to be unwrapped for further usage.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: debeziumConfig{}.Parameters(),
	}, nil
}

func (d *DebeziumProcessor) Configure(ctx context.Context, c config.Config) error {
	cfg := debeziumConfig{}
	err := sdk.ParseConfig(ctx, c, &cfg, cfg.Parameters())
	if err != nil {
		return err
	}

	rr, err := sdk.NewReferenceResolver(cfg.Field)
	if err != nil {
		return cerrors.Errorf("invalid reference: %w", err)
	}

	d.fieldRefRes = rr
	return nil
}

func (d *DebeziumProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		proc, err := d.processRecord(rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, proc)
	}

	return out
}

func (d *DebeziumProcessor) processRecord(rec opencdc.Record) (sdk.ProcessedRecord, error) {
	// record must be structured
	ref, err := d.fieldRefRes.Resolve(&rec)
	if err != nil {
		return nil, cerrors.Errorf("failed resolving reference: %w", err)
	}

	var debeziumEvent opencdc.StructuredData
	switch d := ref.Get().(type) {
	case opencdc.StructuredData:
		debeziumEvent = d
	case map[string]any:
		debeziumEvent = d
	case opencdc.RawData:
		err := json.Unmarshal(d.Bytes(), &debeziumEvent)
		if err != nil {
			return nil, cerrors.Errorf("failed unmarshalling JSON from raw data: %w", err)
		}
	case string:
		err := json.Unmarshal([]byte(d), &debeziumEvent)
		if err != nil {
			return nil, cerrors.Errorf("failed unmarshalling JSON from string: %w", err)
		}
	default:
		return nil, cerrors.Errorf("unexpected data type %T", ref.Get())
	}

	// get payload
	debeziumRec, ok := debeziumEvent["payload"].(map[string]any) // the payload has the debezium record
	if !ok {
		return nil, cerrors.New("data to be unwrapped doesn't contain a payload field")
	}

	// check fields under payload
	err = d.validateRecord(debeziumRec)
	if err != nil {
		return nil, cerrors.Errorf("invalid record: %w", err)
	}

	before, err := d.valueToData(debeziumRec[debeziumFieldBefore])
	if err != nil {
		return nil, cerrors.Errorf("failed to parse field %s: %w", debeziumFieldBefore, err)
	}

	after, err := d.valueToData(debeziumRec[debeziumFieldAfter])
	if err != nil {
		return nil, cerrors.Errorf("failed to parse field %s: %w", debeziumFieldAfter, err)
	}

	op, ok := debeziumRec[debeziumFieldOp].(string)
	if !ok {
		return nil, cerrors.Errorf("%s operation is not a string", op)
	}

	operation, err := d.convertOperation(op)
	if err != nil {
		return nil, cerrors.Errorf("error unwrapping operation: %w", err)
	}

	metadata, err := d.unwrapMetadata(rec, debeziumRec)
	if err != nil {
		return nil, cerrors.Errorf("error unwrapping metadata: %w", err)
	}

	return sdk.SingleRecord{
		Key:       d.unwrapKey(rec.Key),
		Position:  rec.Position,
		Operation: operation,
		Payload: opencdc.Change{
			Before: before,
			After:  after,
		},
		Metadata: metadata,
	}, nil
}

func (d *DebeziumProcessor) valueToData(val any) (opencdc.Data, error) {
	switch v := val.(type) {
	case map[string]any:
		return opencdc.StructuredData(v), nil
	case string:
		return opencdc.RawData(v), nil
	case nil:
		// nil is allowed
		return nil, nil
	default:
		return nil, cerrors.Errorf("expected a map or a string, got %T", val)
	}
}

func (d *DebeziumProcessor) validateRecord(data opencdc.StructuredData) error {
	var errs []error
	if _, ok := data[debeziumFieldAfter]; !ok {
		errs = append(errs, cerrors.Errorf("the %q field is missing from debezium payload", debeziumFieldAfter))
	}
	if _, ok := data[debeziumFieldSource]; !ok {
		errs = append(errs, cerrors.Errorf("the %q field is missing from debezium payload", debeziumFieldSource))
	}
	if _, ok := data[debeziumFieldOp]; !ok {
		errs = append(errs, cerrors.Errorf("the %q field is missing from debezium payload", debeziumFieldOp))
	}
	// ts_ms and transaction can be empty
	return cerrors.Join(errs...)
}

func (d *DebeziumProcessor) unwrapMetadata(rec opencdc.Record, dbzRec opencdc.StructuredData) (opencdc.Metadata, error) {
	var source map[string]string
	for field, val := range dbzRec {
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

func (d *DebeziumProcessor) flatten(key string, val any) map[string]string {
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
func (d *DebeziumProcessor) convertOperation(op string) (opencdc.Operation, error) {
	switch op {
	case debeziumOpCreate:
		return opencdc.OperationCreate, nil
	case debeziumOpUpdate:
		return opencdc.OperationUpdate, nil
	case debeziumOpDelete:
		return opencdc.OperationDelete, nil
	case debeziumOpRead:
		return opencdc.OperationSnapshot, nil
	case debeziumOpUnset:
		return opencdc.OperationUpdate, nil
	}
	return 0, cerrors.Errorf("%q is an invalid operation", op)
}

func (d *DebeziumProcessor) unwrapKey(key opencdc.Data) opencdc.Data {
	// convert the key to structured data
	var structKey opencdc.StructuredData
	switch d := key.(type) {
	case opencdc.RawData:
		// try unmarshalling raw key
		err := json.Unmarshal(key.Bytes(), &structKey)
		// if key is not json formatted, return the original key
		if err != nil {
			return key
		}
	case opencdc.StructuredData:
		structKey = d
	}

	payload, ok := structKey["payload"]
	// return the original key if it doesn't contain a payload
	if !ok {
		return key
	}

	// if payload is a map, return the payload as structured data
	if p, ok := payload.(map[string]any); ok {
		return opencdc.StructuredData(p)
	}

	// otherwise, convert the payload to string, then return it as raw data
	return opencdc.RawData(fmt.Sprint(payload))
}
