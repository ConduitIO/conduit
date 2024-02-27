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

//go:generate paramgen -output=unwrap_kafkaconnect_paramgen.go unwrapKafkaConnectConfig

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

type unwrapKafkaConnectConfig struct {
	// Field is a reference to the field which contains the Kafka Connect record.
	//
	// For more information about record references, see: https://github.com/ConduitIO/conduit-processor-sdk/blob/cbdc5dcb5d3109f8f13b88624c9e360076b0bcdb/util.go#L66.
	Field string `json:"field" validate:"regex=^.Payload" default:".Payload.After"`
}

type unwrapKafkaConnect struct {
	sdk.UnimplementedProcessor

	cfg         unwrapKafkaConnectConfig
	fieldRefRes sdk.ReferenceResolver
}

func newUnwrapKafkaConnect(log.CtxLogger) *unwrapKafkaConnect {
	return &unwrapKafkaConnect{}
}

func (u *unwrapKafkaConnect) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "unwrap.kafkaconnect",
		Summary: "A processors which unwraps a Kafka Connect record from an OpenCDC record.",
		Description: `This processor unwraps a Kafka Connect record from the input OpenCDC record.

This is useful in cases where Conduit acts as an intermediary between a Kafka Connect source and a Kafka Connect destination. 
In such cases, the Kafka Connect record is set as the OpenCDC record's payload, and needs to be unwrapped for further usage.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: unwrapKafkaConnectConfig{}.Parameters(),
	}, nil
}

func (u *unwrapKafkaConnect) Configure(_ context.Context, m map[string]string) error {
	cfg := unwrapKafkaConnectConfig{}
	inputCfg := config.Config(m).Sanitize().ApplyDefaults(cfg.Parameters())
	err := inputCfg.Validate(cfg.Parameters())
	if err != nil {
		return cerrors.Errorf("invalid configuration: %w", err)
	}

	err = inputCfg.DecodeInto(&cfg)
	if err != nil {
		return cerrors.Errorf("failed decoding configuration: %w", err)
	}

	rr, err := sdk.NewReferenceResolver(cfg.Field)
	if err != nil {
		return cerrors.Errorf("invalid reference: %w", err)
	}

	u.fieldRefRes = rr
	return nil
}

func (u *unwrapKafkaConnect) Open(context.Context) error {
	return nil
}

func (u *unwrapKafkaConnect) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
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

func (u *unwrapKafkaConnect) processRecord(rec opencdc.Record) sdk.ProcessedRecord {
	// record must be structured
	ref, err := u.fieldRefRes.Resolve(&rec)
	if err != nil {
		return sdk.ErrorRecord{
			Error: cerrors.Errorf("failed resolving reference: %w", err),
		}
	}

	var kc opencdc.StructuredData
	switch d := ref.Get().(type) {
	case opencdc.StructuredData:
		kc = d
	case map[string]any:
		kc = d
	default:
		return sdk.ErrorRecord{
			Error: cerrors.Errorf("unexpected data type %T (only structured data is supported)", ref.Get()),
		}
	}

	// get payload
	structPayload, ok := kc["payload"].(map[string]any)
	if !ok {
		return sdk.ErrorRecord{Error: cerrors.New("referenced record field doesn't contain a payload field")}
	}

	return sdk.SingleRecord{
		Key:      u.unwrapKey(rec.Key),
		Position: rec.Position,
		Metadata: nil,
		Payload: opencdc.Change{
			Before: nil,
			After:  opencdc.StructuredData(structPayload),
		},
		Operation: opencdc.OperationSnapshot,
	}
}

func (u *unwrapKafkaConnect) unwrapKey(key opencdc.Data) opencdc.Data {
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

func (u *unwrapKafkaConnect) Teardown(context.Context) error {
	return nil
}
