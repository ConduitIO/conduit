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

//go:generate paramgen -output=decode_paramgen.go decodeConfig
//go:generate mockgen -typed -source decode.go -destination=mock_decoder.go -package=avro -mock_names=decoder=MockDecoder . decoder

package avro

import (
	"context"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/avro/internal"
	"github.com/conduitio/conduit/pkg/schemaregistry"
)

type decoder interface {
	Decode(ctx context.Context, b opencdc.RawData) (opencdc.StructuredData, error)
}

type decodeConfig struct {
	// The field that will be decoded.
	//
	// For more information about the format, see [Referencing fields](https://conduit.io/docs/processors/referencing-fields).
	Field string `json:"field" default:".Payload.After"`

	fieldResolver sdk.ReferenceResolver
}

func parseDecodeConfig(ctx context.Context, c config.Config) (decodeConfig, error) {
	cfg := decodeConfig{}
	err := sdk.ParseConfig(ctx, c, &cfg, cfg.Parameters())
	if err != nil {
		return decodeConfig{}, err
	}

	// Parse target field
	rr, err := sdk.NewReferenceResolver(cfg.Field)
	if err != nil {
		return decodeConfig{}, cerrors.Errorf("failed parsing target field: %w", err)
	}
	cfg.fieldResolver = rr

	return cfg, nil
}

type decodeProcessor struct {
	sdk.UnimplementedProcessor

	logger   log.CtxLogger
	cfg      decodeConfig
	decoder  decoder
	registry schemaregistry.Registry
}

func NewDecodeProcessor(logger log.CtxLogger) sdk.Processor {
	return &decodeProcessor{logger: logger}
}

func (p *decodeProcessor) SetSchemaRegistry(registry schemaregistry.Registry) {
	p.registry = registry
}

func (p *decodeProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "avro.decode",
		Summary: "Decodes a field's raw data in the Avro format.",
		Description: `The processor takes raw data (bytes or a string) in the specified field and decodes
it from the [Avro format](https://avro.apache.org/) into structured data. It extracts the schema ID from the data,
downloads the associated schema from the [schema registry](https://docs.confluent.io/platform/current/schema-registry/index.html)
and decodes the payload. The schema is cached locally after it's first downloaded.

If the processor encounters structured data or the data can't be decoded it returns an error.

This processor is the counterpart to [` + "`avro.encode`" + `](/docs/processors/builtin/avro.encode).`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: decodeConfig{}.Parameters(),
	}, nil
}

func (p *decodeProcessor) Configure(ctx context.Context, c config.Config) error {
	cfg, err := parseDecodeConfig(ctx, c)
	if err != nil {
		return cerrors.Errorf("invalid config: %w", err)
	}

	p.cfg = cfg

	return nil
}

func (p *decodeProcessor) Open(context.Context) error {
	p.decoder = internal.NewDecoder(p.registry, p.logger)

	return nil
}

func (p *decodeProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
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

func (p *decodeProcessor) processRecord(ctx context.Context, rec opencdc.Record) (sdk.ProcessedRecord, error) {
	field, err := p.cfg.fieldResolver.Resolve(&rec)
	if err != nil {
		return nil, cerrors.Errorf("failed resolving field: %w", err)
	}

	data, err := p.rawData(field.Get())
	if err != nil {
		return nil, cerrors.Errorf("failed getting raw data: %w", err)
	}

	rd, err := p.decoder.Decode(ctx, data)
	if err != nil {
		return nil, cerrors.Errorf("failed encoding data: %w", err)
	}

	err = field.Set(rd)
	if err != nil {
		return nil, cerrors.Errorf("failed setting the decoded value: %w", err)
	}
	return sdk.SingleRecord(rec), nil
}

func (p *decodeProcessor) rawData(data any) (opencdc.RawData, error) {
	switch v := data.(type) {
	case opencdc.RawData:
		return v, nil
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	default:
		return nil, cerrors.Errorf("unexpected data type %T", v)
	}
}

func (p *decodeProcessor) Teardown(ctx context.Context) error {
	return nil
}
