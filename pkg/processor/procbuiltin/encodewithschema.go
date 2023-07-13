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

// EncodeWithSchemaKey builds a processor with the following config fields:
//   - `url` (Required) - URL of the schema registry (e.g. http://localhost:8085)
//   - `schema.strategy` (Required, Enum: `registry`,`autoRegister`) - Specifies
//     which strategy to use to determine the schema for the record. Available
//     strategies:
//   - `registry` (recommended) - Download an existing schema from the schema
//     registry. This strategy is further configured with options starting
//     with `schema.registry.*`.
//   - `autoRegister` (for development purposes) - Infer the schema from the
//     record and register it in the schema registry. This strategy is further
//     configured with options starting with `schema.autoRegister.*`.
//   - `schema.registry.subject` (Required if `schema.strategy` = `registry`) -
//     Specifies the subject of the schema in the schema registry used to encode
//     the record.
//   - `schema.registry.version` (Required if `schema.strategy` = `registry`) -
//     Specifies the version of the schema in the schema registry used to encode
//     the record.
//   - `schema.autoRegister.subject` (Required if `schema.strategy` = `autoRegister`) -
//     Specifies the subject name under which the inferred schema will be
//     registered in the schema registry.
//   - `schema.autoRegister.format` (Required if `schema.strategy` = `autoRegister`, Enum: `avro`) -
//     Specifies the schema format that should be inferred. Currently the only
//     supported format is `avro`.
//   - `auth.basic.username` (Optional) - Configures the username to use with
//     basic authentication. This option is required if `auth.basic.password`
//     contains a value. If both `auth.basic.username` and `auth.basic.password`
//     are empty basic authentication is disabled.
//   - `auth.basic.password` (Optional) - Configures the password to use with
//     basic authentication. This option is required if `auth.basic.username`
//     contains a value. If both `auth.basic.username` and `auth.basic.password`
//     are empty basic authentication is disabled.
//   - `tls.ca.cert` (Optional) - Path to a file containing PEM encoded CA
//     certificates. If this option is empty, Conduit falls back to using the
//     host's root CA set.
//   - `tls.client.cert` (Optional) - Path to a file containing a PEM encoded
//     certificate. This option is required if `tls.client.key` contains a value.
//     If both `tls.client.cert` and `tls.client.key` are empty TLS is disabled.
//   - `tls.client.key` (Optional) - Path to a file containing a PEM encoded
//     private key. This option is required if `tls.client.cert` contains a value.
//     If both `tls.client.cert` and `tls.client.key` are empty TLS is disabled.
//
// The processor takes structured data and encodes it using a schema into the
// Confluent wire format. It provides two strategies for determining the
// schema:
//
//   - `registry` (recommended)
//
//     This strategy downloads an existing schema from the schema registry and
//     uses it to encode the record. This requires the schema to already be
//     registered in the schema registry. The schema is downloaded only once and
//     cached locally.
//
//   - `autoRegister` (for development purposes)
//     This strategy infers the schema by inspecting the structured data and
//     registers it in the schema registry. If the record schema is known in
//     advance it's recommended to use the `registry` strategy and manually
//     register the schema, as this strategy comes with limitations.
//
//     The strategy uses reflection to traverse the structured data of each
//     record and determine the type of each field. If a specific field is set
//     to `nil` the processor won't have enough information to determine the
//     type and will default to a nullable string. Because of this it is not
//     guaranteed that two records with the same structure produce the same
//     schema or even a backwards compatible schema. The processor registers
//     each inferred schema in the schema registry with the same subject,
//     therefore the schema compatibility checks need to be disabled for this
//     schema to prevent failures. If the schema subject does not exist before
//     running this processor, it will automatically set the correct
//     compatibility settings the first time it registers the schema.
//
// The processor currently only supports the Avro format.
//
// More info about the Confluent wire format: https://docs.confluent.io/platform/current/schema-registry/fundamentals/serdes-develop/index.html#wire-format
// More info about the Confluent schema registry: https://docs.confluent.io/platform/current/schema-registry/index.html
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
