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

//go:generate paramgen -output=kafka_connect_paramgen.go kafkaConnectConfig

package unwrap

import (
	"context"
	"fmt"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
)

type kafkaConnectConfig struct {
	// Field is a reference to the field that contains the Kafka Connect record.
	//
	// For more information about the format, see [Referencing fields](https://conduit.io/docs/processors/referencing-fields).
	Field string `json:"field" validate:"regex=^.Payload" default:".Payload.After"`
}

type kafkaConnectProcessor struct {
	sdk.UnimplementedProcessor

	fieldRefRes sdk.ReferenceResolver
}

func NewKafkaConnectProcessor(log.CtxLogger) sdk.Processor {
	return &kafkaConnectProcessor{}
}

func (u *kafkaConnectProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "unwrap.kafkaconnect",
		Summary: "Unwraps a Kafka Connect record from an [OpenCDC record](https://conduit.io/docs/features/opencdc-record).",
		Description: `This processor unwraps a Kafka Connect record from the input [OpenCDC record](https://conduit.io/docs/features/opencdc-record).

The input record's payload is replaced with the Kafka Connect record.

This is useful in cases where Conduit acts as an intermediary between a Debezium source and a Debezium destination. 
In such cases, the Debezium record is set as the [OpenCDC record](https://conduit.io/docs/features/opencdc-record)'s payload, and needs to be unwrapped for further usage.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: kafkaConnectConfig{}.Parameters(),
	}, nil
}

func (u *kafkaConnectProcessor) Configure(ctx context.Context, c config.Config) error {
	cfg := kafkaConnectConfig{}
	err := sdk.ParseConfig(ctx, c, &cfg, cfg.Parameters())
	if err != nil {
		return err
	}

	rr, err := sdk.NewReferenceResolver(cfg.Field)
	if err != nil {
		return cerrors.Errorf("invalid reference: %w", err)
	}

	u.fieldRefRes = rr
	return nil
}

func (u *kafkaConnectProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
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

func (u *kafkaConnectProcessor) processRecord(rec opencdc.Record) (sdk.ProcessedRecord, error) {
	ref, err := u.fieldRefRes.Resolve(&rec)
	if err != nil {
		return nil, cerrors.Errorf("failed resolving reference: %w", err)
	}

	var kc opencdc.StructuredData
	switch d := ref.Get().(type) {
	case opencdc.StructuredData:
		kc = d
	case map[string]any:
		kc = d
	case opencdc.RawData:
		err := json.Unmarshal(d.Bytes(), &kc)
		if err != nil {
			return nil, cerrors.Errorf("failed unmarshalling JSON from raw data: %w", err)
		}
	case string:
		err := json.Unmarshal([]byte(d), &kc)
		if err != nil {
			return nil, cerrors.Errorf("failed unmarshalling JSON from string: %w", err)
		}
	default:
		return nil, cerrors.Errorf("unexpected data type %T (only structured data is supported)", ref.Get())
	}

	// get payload
	structPayload, ok := kc["payload"].(map[string]any)
	if !ok {
		return nil, cerrors.New("referenced record field doesn't contain a payload field")
	}

	return sdk.SingleRecord{
		Key:      u.unwrapKey(rec.Key),
		Position: rec.Position,
		Metadata: rec.Metadata,
		Payload: opencdc.Change{
			After: opencdc.StructuredData(structPayload),
		},
		Operation: opencdc.OperationCreate,
	}, nil
}

// todo same as in debezium
func (u *kafkaConnectProcessor) unwrapKey(key opencdc.Data) opencdc.Data {
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
	switch p := payload.(type) {
	case opencdc.StructuredData:
		return p
	case map[string]any:
		return opencdc.StructuredData(p)
	default:
		// otherwise, convert the payload to string, then return it as raw data
		return opencdc.RawData(fmt.Sprint(payload))
	}
}
