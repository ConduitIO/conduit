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

package builtin

import (
	"context"
	"fmt"
	"github.com/goccy/go-json"
	"strings"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
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

type unwrapDebezium struct {
	sdk.UnimplementedProcessor

	logger      log.CtxLogger
	fieldRefRes sdk.ReferenceResolver
}

func newUnwrapDebezium(logger log.CtxLogger) sdk.Processor {
	return &unwrapDebezium{logger: logger}
}

func (u *unwrapDebezium) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "unwrap.debezium",
		Summary: "Unwraps a Debezium record from the input OpenCDC record.",
		Description: `This processor unwraps a Debezium record from the input OpenCDC record.

This is useful in cases where Conduit acts as an intermediary between a Debezium source and a Debezium destination. 
In such cases, the Debezium record is set as the OpenCDC record's payload, and needs to be unwrapped for further usage.`,
		Version: "v0.1.0",
		Author:  "Meroxa, Inc.",
		Parameters: config.Parameters{
			"field": {
				Default: ".Payload.After",
				Description: `Reference to the field which contains the wrapped Debezium record.

This should be a valid reference within an OpenCDC record, as specified here: https://github.com/ConduitIO/conduit-processor-sdk/blob/main/util.go#L66
`,
				Type:        config.ParameterTypeString,
				Validations: nil,
			},
		},
	}, nil
}

func (u *unwrapDebezium) Configure(_ context.Context, m map[string]string) error {
	field, ok := m["field"]
	if !ok {
		field = ".Payload.After"
	}

	field = strings.TrimSpace(field)
	if !strings.HasPrefix(field, ".Payload") {
		return cerrors.Errorf("only payload can be unwrapped, field given: %v", field)
	}

	rr, err := sdk.NewReferenceResolver(field)
	if err != nil {
		return cerrors.Errorf("invalid reference: %w", err)
	}

	u.fieldRefRes = rr
	return nil
}

func (u *unwrapDebezium) Open(context.Context) error {
	return nil
}

func (u *unwrapDebezium) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		proc := u.processRecord(rec)
		out = append(out, proc)
		if _, ok := proc.(sdk.ErrorRecord); ok {
			return out
		}
	}

	return out
}

func (u *unwrapDebezium) Teardown(context.Context) error {
	return nil
}

func (u *unwrapDebezium) processRecord(rec opencdc.Record) sdk.ProcessedRecord {
	// record must be structured
	ref, err := u.fieldRefRes.Resolve(&rec)
	if err != nil {
		return sdk.ErrorRecord{
			Error: cerrors.Errorf("failed resolving reference: %w", err),
		}
	}

	var debeziumEvent opencdc.StructuredData
	switch d := ref.Get().(type) {
	case opencdc.RawData:
		// todo: take this section out (use a separate processor for this)
		// unmarshal raw data to structured
		err := json.Unmarshal(d.Bytes(), &debeziumEvent)
		if err != nil {
			return sdk.ErrorRecord{
				Error: cerrors.Errorf("failed to unmarshal raw data as JSON: %w", err),
			}
		}
	case opencdc.StructuredData:
		debeziumEvent = d
	case map[string]any:
		debeziumEvent = d
	default:
		return sdk.ErrorRecord{
			Error: cerrors.Errorf("unexpected data type %T", ref.Get()),
		}
	}

	// get payload
	debeziumRec, ok := debeziumEvent["payload"].(map[string]any) // the payload has the debezium record
	if !ok {
		return sdk.ErrorRecord{
			Error: cerrors.New("data to be unwrapped doesn't contain a payload field"),
		}
	}

	// check fields under payload
	err = u.validateRecord(debeziumRec)
	if err != nil {
		return sdk.ErrorRecord{
			Error: cerrors.Errorf("invalid record: %w", err),
		}
	}

	before, err := u.valueToData(debeziumRec[debeziumFieldBefore])
	if err != nil {
		return sdk.ErrorRecord{
			Error: cerrors.Errorf("failed to parse field %s: %w", debeziumFieldBefore, err),
		}
	}

	after, err := u.valueToData(debeziumRec[debeziumFieldAfter])
	if err != nil {
		return sdk.ErrorRecord{
			Error: cerrors.Errorf("failed to parse field %s: %w", debeziumFieldAfter, err),
		}
	}

	op, ok := debeziumRec[debeziumFieldOp].(string)
	if !ok {
		return sdk.ErrorRecord{
			Error: cerrors.Errorf("%s operation is not a string", op),
		}
	}

	operation, err := u.convertOperation(op)
	if err != nil {
		return sdk.ErrorRecord{
			Error: cerrors.Errorf("error unwrapping operation: %w", err),
		}
	}

	metadata, err := u.unwrapMetadata(rec, debeziumRec)
	if err != nil {
		return sdk.ErrorRecord{
			Error: cerrors.Errorf("error unwrapping metadata: %w", err),
		}
	}

	return sdk.SingleRecord{
		Key:       u.unwrapKey(rec.Key),
		Position:  rec.Position,
		Operation: operation,
		Payload: opencdc.Change{
			Before: before,
			After:  after,
		},
		Metadata: metadata,
	}
}

func (d *unwrapDebezium) valueToData(val any) (opencdc.Data, error) {
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

func (d *unwrapDebezium) validateRecord(data opencdc.StructuredData) error {
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

func (d *unwrapDebezium) unwrapMetadata(rec opencdc.Record, dbzRec opencdc.StructuredData) (opencdc.Metadata, error) {
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

func (d *unwrapDebezium) flatten(key string, val any) map[string]string {
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
func (d *unwrapDebezium) convertOperation(op string) (opencdc.Operation, error) {
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

func (u *unwrapDebezium) unwrapKey(key opencdc.Data) opencdc.Data {
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
