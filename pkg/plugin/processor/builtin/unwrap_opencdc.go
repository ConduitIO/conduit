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
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
)

type unwrapOpenCDC struct {
	sdk.UnimplementedProcessor

	logger      log.CtxLogger
	fieldRefRes sdk.ReferenceResolver
}

func newUnwrapOpenCDC(logger log.CtxLogger) sdk.Processor {
	return &unwrapOpenCDC{logger: logger}
}

func (u *unwrapOpenCDC) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "unwrap.opencdc",
		Summary: "A processor that unwraps the OpenCDC record saved in one of record's fields.",
		Description: `
The unwrap.opencdc processors unwraps the OpenCDC record saved in one of the record's fields.
This is useful in situations where a record goes through intermediate systems before 
being written to a final destination. In these cases, the original OpenCDC record is
part of the payload read from the intermediate system and needs to be unwrapped before
being written to the destination.
'`,
		Version: "v0.1.0",
		Author:  "Meroxa, Inc.",
		Parameters: config.Parameters{
			"field": config.Parameter{
				Default: ".Payload.After",
				Description: `
A reference to the field which contains the OpenCDC record.

For more information about record references, see: https://github.com/ConduitIO/conduit-processor-sdk/blob/cbdc5dcb5d3109f8f13b88624c9e360076b0bcdb/util.go#L66.`,
				Type: config.ParameterTypeString,
			},
		},
	}, nil
}

func (u *unwrapOpenCDC) Configure(_ context.Context, m map[string]string) error {
	field, ok := m["field"]
	if !ok {
		return cerrors.New("missing required parameter 'field'")
	}

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

func (u *unwrapOpenCDC) Open(context.Context) error {
	return nil
}

func (u *unwrapOpenCDC) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
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

func (u *unwrapOpenCDC) processRecord(rec opencdc.Record) sdk.ProcessedRecord {
	ref, err := u.fieldRefRes.Resolve(&rec)
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("failed resolving record reference: %w", err)}
	}

	var data opencdc.StructuredData
	switch v := ref.Get().(type) {
	case opencdc.RawData:
		// unmarshal raw data to structured
		if err := json.Unmarshal(v.Bytes(), &data); err != nil {
			return sdk.ErrorRecord{Error: cerrors.Errorf("failed to unmarshal raw data as JSON: %w", err)}
		}
	case opencdc.StructuredData:
		data = v
	case nil:
		return sdk.ErrorRecord{Error: cerrors.New("field to unmarshal is nil")}
	default:
		return sdk.ErrorRecord{Error: cerrors.Errorf("unexpected data type %T", v)}
	}

	opencdcRec, err := u.unmarshalRecord(data)
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("failed unmarshalling record: %w", err)}
	}
	opencdcRec.Position = rec.Position

	// Position is the only key we preserve from the original record to maintain the reference respect other messages
	// that will be coming from in the event of chaining pipelines (e.g.: source -> kafka, kafka -> destination)
	return sdk.SingleRecord(opencdcRec)
}

func (u *unwrapOpenCDC) Teardown(context.Context) error {
	return nil
}

func (u *unwrapOpenCDC) unmarshalRecord(structData opencdc.StructuredData) (opencdc.Record, error) {
	operation, err := u.unmarshalOperation(structData)
	if err != nil {
		return opencdc.Record{}, cerrors.Errorf("failed unmarshalling operation: %w", err)
	}

	metadata, err := u.unmarshalMetadata(structData)
	if err != nil {
		return opencdc.Record{}, cerrors.Errorf("failed unmarshalling metadata: %w", err)
	}

	key, err := u.unmarshalKey(structData)
	if err != nil {
		return opencdc.Record{}, cerrors.Errorf("failed unmarshalling key: %w", err)
	}

	payload, err := u.unmarshalPayload(structData)
	if err != nil {
		return opencdc.Record{}, cerrors.Errorf("failed unmarshalling payload: %w", err)
	}

	return opencdc.Record{
		Key:       key,
		Metadata:  metadata,
		Payload:   payload,
		Operation: operation,
	}, nil
}

// unmarshalOperation extracts operation from a structuredData record.
func (u *unwrapOpenCDC) unmarshalOperation(structData opencdc.StructuredData) (opencdc.Operation, error) {
	var operation opencdc.Operation
	op, ok := structData["operation"]
	if !ok {
		return operation, cerrors.New("no operation")
	}

	switch opType := op.(type) {
	case opencdc.Operation:
		operation = opType
	case string:
		if err := operation.UnmarshalText([]byte(opType)); err != nil {
			return operation, cerrors.Errorf("invalid operation %q", opType)
		}
	default:
		return operation, cerrors.Errorf("expected a opencdc.Operation or a string, got %T", opType)
	}
	return operation, nil
}

// unmarshalMetadata extracts metadata from a structuredData record.
func (u *unwrapOpenCDC) unmarshalMetadata(structData opencdc.StructuredData) (opencdc.Metadata, error) {
	var metadata opencdc.Metadata
	meta, ok := structData["metadata"]
	if !ok {
		return metadata, cerrors.New("no metadata")
	}

	switch m := meta.(type) {
	case opencdc.Metadata:
		metadata = m
	case map[string]interface{}:
		metadata = make(opencdc.Metadata, len(m))
		for k, v := range m {
			metadata[k] = fmt.Sprint(v)
		}
	default:
		return metadata, cerrors.Errorf("expected a opencdc.Metadata or a map[string]interface{}, got %T", m)
	}
	return metadata, nil
}

// unmarshalKey extracts key from a structuredData record.
func (u *unwrapOpenCDC) unmarshalKey(structData opencdc.StructuredData) (opencdc.Data, error) {
	var key opencdc.Data
	ky, ok := structData["key"]
	if !ok {
		return key, cerrors.New("no key")
	}
	switch k := ky.(type) {
	case map[string]interface{}:
		convertedData := make(opencdc.StructuredData, len(k))
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
		key = opencdc.RawData(decoded[:n])
	default:
		return key, cerrors.Errorf("expected a opencdc.Data or a string, got %T", k)
	}
	return key, nil
}

func (u *unwrapOpenCDC) convertPayloadData(payload map[string]interface{}, key string) (opencdc.Data, error) {
	payloadData, ok := payload[key]
	if !ok {
		return nil, nil
	}

	switch data := payloadData.(type) {
	case map[string]interface{}:
		convertedData := make(opencdc.StructuredData, len(data))
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
		return opencdc.RawData(decoded[:n]), nil
	default:
		return nil, nil
	}
}

// unmarshalPayload extracts payload from a structuredData record.
func (u *unwrapOpenCDC) unmarshalPayload(structData opencdc.StructuredData) (opencdc.Change, error) {
	var payload opencdc.Change
	pl, ok := structData["payload"]
	if !ok {
		return payload, cerrors.New("no payload")
	}

	switch p := pl.(type) {
	case opencdc.Change:
		payload = p
	case map[string]interface{}:
		before, err := u.convertPayloadData(p, "before")
		if err != nil {
			return opencdc.Change{}, err
		}

		after, err := u.convertPayloadData(p, "after")
		if err != nil {
			return opencdc.Change{}, err
		}

		payload = opencdc.Change{
			Before: before,
			After:  after,
		}
	default:
		return opencdc.Change{}, cerrors.Errorf("expected a opencdc.Change or a map[string]interface{}, got %T", p)
	}
	return payload, nil
}
