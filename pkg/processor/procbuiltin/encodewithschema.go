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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/schemaregistry"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/lovromazgon/franz-go/pkg/sr"
	"github.com/rs/zerolog"
)

const (
	encodeWithSchemaKeyProcType     = "encodewithschemakey"
	encodeWithSchemaPayloadProcType = "encodewithschemapayload"

	encodeWithSchemaStrategy            = "schema.strategy"
	encodeWithSchemaRegistrySubject     = "schema.registry.subject"
	encodeWithSchemaRegistryVersion     = "schema.registry.version"
	encodeWithSchemaAutoRegisterSubject = "schema.autoRegister.subject"
	encodeWithSchemaAutoRegisterFormat  = "schema.autoRegister.format"

	encodeWithSchemaStrategyRegistry     = "registry"
	encodeWithSchemaStrategyAutoRegister = "autoRegister"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(encodeWithSchemaKeyProcType, EncodeWithSchemaKey)
	processor.GlobalBuilderRegistry.MustRegister(encodeWithSchemaPayloadProcType, EncodeWithSchemaPayload)
}

// EncodeWithSchemaKey builds the following processor:
// TODO
func EncodeWithSchemaKey(config processor.Config) (processor.Interface, error) {
	return encodeWithSchema(encodeWithSchemaKeyProcType, recordKeyGetSetter{}, config)
}

// EncodeWithSchemaPayload builds the same processor as EncodeWithSchemaKey,
// except that it operates on the field Record.Payload.After.
func EncodeWithSchemaPayload(config processor.Config) (processor.Interface, error) {
	return encodeWithSchema(encodeWithSchemaPayloadProcType, recordPayloadGetSetter{}, config)
}

type encodeWithSchemaConfig struct {
	schemaRegistryConfig
	strategy schemaregistry.SchemaStrategy
}

func (c *encodeWithSchemaConfig) Parse(cfg processor.Config) error {
	if err := c.schemaRegistryConfig.Parse(cfg); err != nil {
		return err
	}
	return c.parseSchemaStrategy(cfg)
}

func (c *encodeWithSchemaConfig) parseSchemaStrategy(cfg processor.Config) error {
	strategy, err := getConfigFieldString(cfg, encodeWithSchemaStrategy)
	if err != nil {
		return err
	}

	switch strategy {
	case encodeWithSchemaStrategyRegistry:
		return c.parseSchemaStrategyRegistry(cfg)
	case encodeWithSchemaStrategyAutoRegister:
		return c.parseSchemaStrategyAutoRegister(cfg)
	default:
		return cerrors.Errorf("failed to parse %q: unknown schema strategy %q", encodeWithSchemaStrategy, strategy)
	}
}

func (c *encodeWithSchemaConfig) parseSchemaStrategyRegistry(cfg processor.Config) error {
	subject, err := getConfigFieldString(cfg, encodeWithSchemaRegistrySubject)
	if err != nil {
		return err
	}
	// TODO allow version to be set to "latest"
	version, err := getConfigFieldInt64(cfg, encodeWithSchemaRegistryVersion)
	if err != nil {
		return err
	}
	c.strategy = schemaregistry.DownloadSchemaStrategy{
		Subject: subject,
		Version: int(version),
	}
	return nil
}

func (c *encodeWithSchemaConfig) parseSchemaStrategyAutoRegister(cfg processor.Config) error {
	subject, err := getConfigFieldString(cfg, encodeWithSchemaAutoRegisterSubject)
	if err != nil {
		return err
	}
	format, err := getConfigFieldString(cfg, encodeWithSchemaAutoRegisterFormat)
	if err != nil {
		return err
	}
	var schemaType sr.SchemaType
	err = schemaType.UnmarshalText([]byte(format))
	if err != nil {
		return cerrors.Errorf("failed to parse %q: %w", encodeWithSchemaAutoRegisterSubject, err)
	}
	c.strategy = schemaregistry.ExtractAndUploadSchemaStrategy{
		Type:    schemaType,
		Subject: subject,
	}
	return nil
}

func (c *encodeWithSchemaConfig) SchemaStrategy() schemaregistry.SchemaStrategy {
	return c.strategy
}

func encodeWithSchema(
	processorType string,
	getSetter recordDataGetSetter,
	config processor.Config,
) (processor.Interface, error) {
	var c encodeWithSchemaConfig
	err := c.Parse(config)
	if err != nil {
		return nil, cerrors.Errorf("%s: %w", processorType, err)
	}

	// TODO get logger from config or some other place
	logger := log.InitLogger(zerolog.InfoLevel, log.FormatCLI)

	client, err := schemaregistry.NewClient(logger, c.ClientOptions()...)
	if err != nil {
		return nil, cerrors.Errorf("%s: could not create schema registry client: %w", processorType, err)
	}
	encoder := schemaregistry.NewEncoder(client, logger, &sr.Serde{}, c.SchemaStrategy())

	return NewFuncWrapper(func(ctx context.Context, r record.Record) (record.Record, error) {
		data := getSetter.Get(r)

		switch d := data.(type) {
		case record.RawData:
			return record.Record{}, cerrors.Errorf("%s: raw data not supported (hint: if your records carry JSON data you can parse them into structured data with the processor `parsejsonpayload`)", processorType)
		case record.StructuredData:
			rd, err := encoder.Encode(ctx, d)
			if err != nil {
				return record.Record{}, cerrors.Errorf("%s: %w:", processorType, err)
			}
			r = getSetter.Set(r, rd)
			return r, nil
		default:
			return record.Record{}, cerrors.Errorf("%s: unexpected data type %T", processorType, data)
		}
	}), nil
}
