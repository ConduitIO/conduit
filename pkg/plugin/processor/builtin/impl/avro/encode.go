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

//go:generate paramgen -output=encode_paramgen.go encodeConfig
//go:generate mockgen -typed -source encode.go -destination=mock_encoder.go -package=avro -mock_names=encoder=MockEncoder . encoder

package avro

import (
	"context"
	"maps"
	"strings"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/avro/internal"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/errsafe"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/goccy/go-json"
)

type encoder interface {
	Encode(ctx context.Context, sd opencdc.StructuredData) (opencdc.RawData, error)
}

type encodeConfig struct {
	// The field that will be encoded.
	//
	// For more information about the format, see [Referencing fields](https://conduitdata.io/docs/using/processors/referencing-fields).
	Field string `json:"field" default:".Payload.After"`

	Schema schemaConfig `json:"schema"`

	fieldResolver sdk.ReferenceResolver
}

func parseEncodeConfig(ctx context.Context, c config.Config) (encodeConfig, error) {
	cfg := encodeConfig{}
	err := sdk.ParseConfig(ctx, c, &cfg, cfg.Parameters())
	if err != nil {
		return encodeConfig{}, err
	}

	err = cfg.Schema.parse()
	if err != nil {
		return encodeConfig{}, cerrors.Errorf("failed parsing schema strategy: %w", err)
	}

	// Parse target field
	rr, err := sdk.NewReferenceResolver(cfg.Field)
	if err != nil {
		return encodeConfig{}, cerrors.Errorf("failed parsing target field: %w", err)
	}
	cfg.fieldResolver = rr

	return cfg, nil
}

type EncodeProcessor struct {
	sdk.UnimplementedProcessor

	logger   log.CtxLogger
	cfg      encodeConfig
	encoder  encoder
	registry schemaregistry.Registry
}

func NewEncodeProcessor(logger log.CtxLogger) *EncodeProcessor {
	return &EncodeProcessor{logger: logger}
}

func (p *EncodeProcessor) SetSchemaRegistry(registry schemaregistry.Registry) {
	p.registry = registry
}

func (p *EncodeProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "avro.encode",
		Summary: "Encodes a record's field into the Avro format.",
		Description: `The processor takes a record's field and encodes it using a schema into the [Avro format](https://avro.apache.org/).
It provides two strategies for determining the schema:

* **preRegistered** (recommended)
  This strategy downloads an existing schema from the schema registry and uses it to encode the record.
  This requires the schema to already be registered in the schema registry. The schema is downloaded
  only once and cached locally.
* **autoRegister** (for development purposes)
  This strategy infers the schema by inspecting the structured data and registers it in the schema
  registry. If the record schema is known in advance it's recommended to use the preRegistered strategy
  and manually register the schema, as this strategy comes with limitations.

  The strategy uses reflection to traverse the structured data of each record and determine the type
  of each field. If a specific field is set to nil the processor won't have enough information to determine
  the type and will default to a nullable string. Because of this it is not guaranteed that two records
  with the same structure produce the same schema or even a backwards compatible schema. The processor
  registers each inferred schema in the schema registry with the same subject, therefore the schema compatibility
  checks need to be disabled for this schema to prevent failures. If the schema subject does not exist before running
  this processor, it will automatically set the correct compatibility settings in the schema registry.

When the encoded field is the whole key or the whole payload (` + "`.Key`, `.Payload.After`, `.Payload.Before`" + `),
the record's schema metadata for that part (` + "`opencdc.key.schema.*`" + ` or ` + "`opencdc.payload.schema.*`" + `) is removed, because
it described the value that was replaced. The encoded bytes carry the schema ID in the registry wire format.
A destination's schema extraction therefore does not decode them a second time.

This processor is the counterpart to [` + "`avro.decode`" + `](/docs/using/processors/builtin/avro.decode).`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: encodeConfig{}.Parameters(),
	}, nil
}

func (p *EncodeProcessor) Configure(ctx context.Context, c config.Config) error {
	cfg, err := parseEncodeConfig(ctx, c)
	if err != nil {
		return cerrors.Errorf("invalid config: %w", err)
	}

	p.cfg = cfg

	return nil
}

func (p *EncodeProcessor) Open(context.Context) error {
	p.encoder = internal.NewEncoder(p.registry, p.logger, p.cfg.Schema.strategy)

	return nil
}

func (p *EncodeProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		proc, err := p.processRecord(ctx, rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}

		out = append(out, proc)
	}

	return out
}

func (p *EncodeProcessor) processRecord(ctx context.Context, rec opencdc.Record) (sdk.ProcessedRecord, error) {
	field, err := p.cfg.fieldResolver.Resolve(&rec)
	if err != nil {
		return nil, cerrors.Errorf("failed resolving field: %w", err)
	}

	data, err := p.structuredData(field.Get())
	if err != nil {
		return nil, cerrors.Errorf("failed getting structured data: %w", err)
	}

	rd, err := p.encoder.Encode(ctx, data)
	if err != nil {
		return nil, cerrors.Errorf("failed encoding data: %w", err)
	}

	err = field.Set(rd)
	if err != nil {
		return nil, cerrors.Errorf("failed setting encoded value into the record: %w", err)
	}
	rec.Metadata = p.dropStaleSchemaMetadata(rec.Metadata)
	return sdk.SingleRecord(rec), nil
}

// dropStaleSchemaMetadata removes the schema subject and version metadata that
// described the field before it was encoded.
//
// Invariant 6: when the processor replaces a whole payload part or the key with
// Avro bytes in the registry wire format, the subject and version the source
// attached describe the value that was just replaced, not these bytes. Left in
// place, a downstream SDK schema-extraction middleware (Kafka and other
// destinations) would decode the already-encoded bytes a second time with the
// source schema, failing every record or, worse, reading them as the wrong
// type. The wire format is self-describing (it carries the schema ID), so a
// destination that wants the schema reads it from there. Writing the encoded
// schema into the metadata instead would not help: the middleware unmarshals
// plain Avro and does not understand the wire-format header.
//
// Only a field that is the whole key or the whole of payload.before/after is
// affected. A nested field (.Payload.After.foo) leaves the record structured
// and the SDK schema-encode middleware keeps managing its schema.
func (p *EncodeProcessor) dropStaleSchemaMetadata(md opencdc.Metadata) opencdc.Metadata {
	var keys [2]string
	switch strings.TrimSpace(p.cfg.Field) {
	case ".Key":
		keys = [2]string{opencdc.MetadataKeySchemaSubject, opencdc.MetadataKeySchemaVersion}
	case ".Payload.After", ".Payload.Before":
		keys = [2]string{opencdc.MetadataPayloadSchemaSubject, opencdc.MetadataPayloadSchemaVersion}
	default:
		return md
	}
	_, hasSubject := md[keys[0]]
	_, hasVersion := md[keys[1]]
	if !hasSubject && !hasVersion {
		return md
	}
	// The map is shared with the input record; do not mutate it.
	md = maps.Clone(md)
	delete(md, keys[0])
	delete(md, keys[1])
	return md
}

func (p *EncodeProcessor) Teardown(context.Context) error {
	return nil
}

func (p *EncodeProcessor) structuredData(data any) (opencdc.StructuredData, error) {
	var sd opencdc.StructuredData
	switch v := data.(type) {
	case opencdc.RawData:
		b := v.Bytes()
		// if data is empty, then return empty structured data
		if len(b) == 0 {
			return sd, nil
		}
		err := json.Unmarshal(b, &sd)
		if err != nil {
			return nil, cerrors.Errorf("failed unmarshalling JSON from raw data: %w", errsafe.Sanitize(err))
		}
	case string:
		err := json.Unmarshal([]byte(v), &sd)
		if err != nil {
			return nil, cerrors.Errorf("failed unmarshalling JSON from raw data: %w", errsafe.Sanitize(err))
		}
	case []byte:
		err := json.Unmarshal(v, &sd)
		if err != nil {
			return nil, cerrors.Errorf("failed unmarshalling JSON from raw data: %w", errsafe.Sanitize(err))
		}
	case opencdc.StructuredData:
		sd = v
	default:
		return nil, cerrors.Errorf("unexpected data type %T", v)
	}

	return sd, nil
}
