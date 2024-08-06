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

//go:generate paramgen -output=opencdc_paramgen.go openCDCConfig

package unwrap

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
)

type openCDCConfig struct {
	// Field is a reference to the field that contains the OpenCDC record.
	//
	// For more information about the format, see [Referencing fields](https://conduit.io/docs/processors/referencing-fields).
	Field string `json:"field" default:".Payload.After"`
}

type openCDCProcessor struct {
	sdk.UnimplementedProcessor

	logger      log.CtxLogger
	fieldRefRes sdk.ReferenceResolver
}

func NewOpenCDCProcessor(logger log.CtxLogger) sdk.Processor {
	return &openCDCProcessor{logger: logger}
}

func (u *openCDCProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "unwrap.opencdc",
		Summary: "Unwraps an [OpenCDC record](https://conduit.io/docs/features/opencdc-record) saved in one of the record's fields.",
		Description: `The ` + "`unwrap.opencdc`" + ` processor is useful in situations where a record goes through intermediate
systems before being written to a final destination. In these cases, the original [OpenCDC record](https://conduit.io/docs/features/opencdc-record) is part of the payload
read from the intermediate system and needs to be unwrapped before being written.

Note: if the wrapped [OpenCDC record](https://conduit.io/docs/features/opencdc-record) is not in a structured data field, then it's assumed that it's stored in JSON format.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: openCDCConfig{}.Parameters(),
	}, nil
}

func (u *openCDCProcessor) Configure(ctx context.Context, c config.Config) error {
	cfg := openCDCConfig{}
	err := sdk.ParseConfig(ctx, c, &cfg, cfg.Parameters())
	if err != nil {
		return cerrors.Errorf("failed parsing configuration: %w", err)
	}

	rr, err := sdk.NewReferenceResolver(cfg.Field)
	if err != nil {
		return cerrors.Errorf("invalid reference: %w", err)
	}

	u.fieldRefRes = rr
	return nil
}

func (u *openCDCProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		proc, err := u.processRecord(rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, proc)
	}

	return out
}

func (u *openCDCProcessor) processRecord(rec opencdc.Record) (sdk.ProcessedRecord, error) {
	ref, err := u.fieldRefRes.Resolve(&rec)
	if err != nil {
		return nil, cerrors.Errorf("failed resolving record reference: %w", err)
	}

	var data opencdc.StructuredData
	switch v := ref.Get().(type) {
	case opencdc.RawData:
		// unmarshal raw data to structured
		if err := json.Unmarshal(v.Bytes(), &data); err != nil {
			return nil, cerrors.Errorf("failed to unmarshal raw data as JSON: %w", err)
		}
	case string:
		// unmarshal raw data to structured
		if err := json.Unmarshal([]byte(v), &data); err != nil {
			return nil, cerrors.Errorf("failed to unmarshal raw data as JSON: %w", err)
		}
	case opencdc.StructuredData:
		data = v
	case nil:
		return nil, cerrors.New("field to unmarshal is nil")
	default:
		return nil, cerrors.Errorf("unexpected data type %T", v)
	}

	opencdcRec, err := u.unmarshalRecord(data)
	if err != nil {
		return nil, cerrors.Errorf("failed unmarshalling record: %w", err)
	}
	// Position is the only key we preserve from the original record to maintain the reference respect other messages
	// that will be coming from in the event of chaining pipelines (e.g.: source -> kafka, kafka -> destination)
	opencdcRec.Position = rec.Position

	return sdk.SingleRecord(opencdcRec), nil
}

func (u *openCDCProcessor) unmarshalRecord(structData opencdc.StructuredData) (opencdc.Record, error) {
	operation, err := u.unmarshalOperation(structData)
	if err != nil {
		return opencdc.Record{}, cerrors.Errorf("failed unmarshalling operation: %w", err)
	}

	metadata, err := u.unmarshalMetadata(structData)
	if err != nil {
		return opencdc.Record{}, cerrors.Errorf("failed unmarshalling metadata: %w", err)
	}

	key, err := u.convertData(structData, "key")
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
func (u *openCDCProcessor) unmarshalOperation(structData opencdc.StructuredData) (opencdc.Operation, error) {
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
func (u *openCDCProcessor) unmarshalMetadata(structData opencdc.StructuredData) (opencdc.Metadata, error) {
	var metadata opencdc.Metadata
	meta, ok := structData["metadata"]
	if !ok {
		return nil, cerrors.New("no metadata")
	}

	switch m := meta.(type) {
	case opencdc.Metadata:
		metadata = m
	case map[string]interface{}:
		metadata = make(opencdc.Metadata, len(m))
		for k, v := range m {
			// if it's already a string, then fmt.Sprint() will be slower
			if str, ok := v.(string); ok {
				metadata[k] = str
			} else {
				metadata[k] = fmt.Sprint(v)
			}
		}
	default:
		return nil, cerrors.Errorf("expected a opencdc.Metadata or a map[string]interface{}, got %T", m)
	}

	return metadata, nil
}

func (u *openCDCProcessor) convertData(m map[string]interface{}, key string) (opencdc.Data, error) {
	data, ok := m[key]
	if !ok || data == nil {
		return nil, nil
	}

	switch d := data.(type) {
	case opencdc.Data:
		return d, nil
	case map[string]interface{}:
		return opencdc.StructuredData(d), nil
	case string:
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(d)))
		n, err := base64.StdEncoding.Decode(decoded, []byte(d))
		if err != nil {
			return nil, cerrors.Errorf("couldn't decode payload %s: %w", err, key)
		}
		return opencdc.RawData(decoded[:n]), nil
	default:
		return nil, cerrors.Errorf("expected a map[string]interface{} or string, got: %T", d)
	}
}

// unmarshalPayload extracts payload from a structuredData record.
func (u *openCDCProcessor) unmarshalPayload(structData opencdc.StructuredData) (opencdc.Change, error) {
	var payload opencdc.Change
	pl, ok := structData["payload"]
	if !ok {
		return payload, cerrors.New("no payload")
	}

	switch p := pl.(type) {
	case opencdc.Change:
		payload = p
	case map[string]interface{}:
		before, err := u.convertData(p, "before")
		if err != nil {
			return opencdc.Change{}, err
		}

		after, err := u.convertData(p, "after")
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
