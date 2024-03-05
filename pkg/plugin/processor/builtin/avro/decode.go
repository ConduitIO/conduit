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
//go:generate mockgen -source decode.go -destination=mock_decoder.go -package=avro -mock_names=decoder=MockDecoder . decoder

package avro

import (
	"context"
	"crypto/tls"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/avro/schemaregistry"
	"github.com/lovromazgon/franz-go/pkg/sr"
)

type decoder interface {
	Decode(ctx context.Context, b opencdc.RawData) (opencdc.StructuredData, error)
}

type decodeConfig struct {
	// The field that will be encoded.
	Field string `json:"field" default:".Payload.After"`

	// URL of the schema registry (e.g. http://localhost:8085)
	URL string `json:"url" validate:"required"`

	Auth authConfig `json:"auth"`
	TLS  tlsConfig  `json:"tls"`

	fieldResolver sdk.ReferenceResolver
}

func parseDecodeConfig(ctx context.Context, m map[string]string) (decodeConfig, error) {
	cfg := decodeConfig{}
	err := sdk.ParseConfig(ctx, m, &cfg, cfg.Parameters())
	if err != nil {
		return decodeConfig{}, err
	}

	err = cfg.Auth.validate()
	if err != nil {
		return decodeConfig{}, cerrors.Errorf("invalid basic auth: %w", err)
	}

	err = cfg.TLS.parse()
	if err != nil {
		return decodeConfig{}, cerrors.Errorf("failed parsing TLS: %w", err)
	}

	// Parse target field
	rr, err := sdk.NewReferenceResolver(cfg.Field)
	if err != nil {
		return decodeConfig{}, cerrors.Errorf("failed parsing target field: %w", err)
	}
	cfg.fieldResolver = rr

	return cfg, nil
}

func (c decodeConfig) ClientOptions() []sr.Opt {
	clientOpts := []sr.Opt{sr.URLs(c.URL), sr.Normalize()}
	if c.Auth.Username != "" && c.Auth.Password != "" {
		clientOpts = append(clientOpts, sr.BasicAuth(c.Auth.Username, c.Auth.Password))
	}

	if c.TLS.tlsClientCert != nil {
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{*c.TLS.tlsClientCert},
			MinVersion:   tls.VersionTLS12,
		}
		if c.TLS.tlsCACert != nil {
			tlsCfg.RootCAs = c.TLS.tlsCACert
		}
		clientOpts = append(clientOpts, sr.DialTLSConfig(tlsCfg))
	}

	return clientOpts
}

type decodeProcessor struct {
	sdk.UnimplementedProcessor

	logger  log.CtxLogger
	cfg     decodeConfig
	decoder decoder
}

func NewDecodeProcessor(logger log.CtxLogger) sdk.Processor {
	return &decodeProcessor{logger: logger}
}

func (p *decodeProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "avro.encode",
		Summary: "Decodes a record's field from the ",
		Description: `The processor takes raw data (bytes) in the specified field and decodes it from the 
[Avro format](https://avro.apache.org/) into structured data. It extracts the schema ID from the data, 
downloads the associated schema from the [schema registry](https://docs.confluent.io/platform/current/schema-registry/index.html)
and decodes the payload. The schema is cached locally after it's first downloaded. 
Currently, the processor only supports the Avro format. If the processor encounters structured data or the data 
can't be decoded it returns an error.

This processor is the counterpart to 'avro.encode'.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: decodeConfig{}.Parameters(),
	}, nil
}

func (p *decodeProcessor) Configure(ctx context.Context, m map[string]string) error {
	cfg, err := parseDecodeConfig(ctx, m)
	if err != nil {
		return cerrors.Errorf("invalid config: %w", err)
	}

	p.cfg = cfg

	return nil
}

func (p *decodeProcessor) Open(ctx context.Context) error {
	client, err := schemaregistry.NewClient(p.logger, p.cfg.ClientOptions()...)
	if err != nil {
		return cerrors.Errorf("could not create schema registry client: %w", err)
	}
	p.decoder = schemaregistry.NewDecoder(client, p.logger, &sr.Serde{})

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
		return nil, cerrors.Errorf("failed getting structured data: %w", err)
	}

	rd, err := p.decoder.Decode(ctx, data)
	if err != nil {
		return nil, cerrors.Errorf("failed encoding data: %w", err)
	}

	err = field.Set(rd)
	if err != nil {
		return nil, cerrors.Errorf("failed setting encoded value into the record: %w", err)
	}
	return sdk.SingleRecord(rec), nil
}

func (p *decodeProcessor) rawData(data any) (opencdc.RawData, error) {
	switch v := data.(type) {
	case opencdc.RawData:
		return v, nil
	case []byte:
		return v, nil
	case opencdc.StructuredData:
		return v.Bytes(), nil
	default:
		return nil, cerrors.Errorf("unexpected data type %T", v)
	}
}

func (p *decodeProcessor) Teardown(ctx context.Context) error {
	//TODO implement me
	panic("implement me")
}
