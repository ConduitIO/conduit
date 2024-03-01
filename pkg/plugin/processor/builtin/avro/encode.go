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

package avro

import (
	"context"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/avro/schemaregistry"
	"github.com/lovromazgon/franz-go/pkg/sr"
)

type encodeProcessor struct {
	sdk.UnimplementedProcessor

	cfg    encodeConfig
	logger log.CtxLogger

	encoder *schemaregistry.Encoder
}

func NewEncodeProcessor(logger log.CtxLogger) sdk.Processor {
	return &encodeProcessor{logger: logger}
}

func (p *encodeProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "encode.avro",
		Summary: "",
		Description: `The processor takes structured data and encodes it using a schema into the [Avro format](https://avro.apache.org/).
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
`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: encodeConfig{}.Parameters(),
	}, nil
}

func (p *encodeProcessor) Configure(ctx context.Context, m map[string]string) error {
	cfg := encodeConfig{}
	err := sdk.ParseConfig(ctx, m, &cfg, cfg.Parameters())
	if err != nil {
		return err
	}

	err = cfg.validateBasicAuth()
	if err != nil {
		return err
	}

	err = cfg.parseTLS()
	if err != nil {
		return err
	}

	err = cfg.parseSchemaStrategy()
	if err != nil {
		return err
	}

	p.cfg = cfg

	return nil
}

func (p *encodeProcessor) Open(ctx context.Context) error {
	client, err := schemaregistry.NewClient(p.logger, p.cfg.ClientOptions()...)
	if err != nil {
		return cerrors.Errorf("could not create schema registry client: %w", err)
	}
	p.encoder = schemaregistry.NewEncoder(client, p.logger, &sr.Serde{}, p.cfg.strategy)

	return nil
}

func (p *encodeProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	//TODO implement me
	panic("implement me")
}

func (p *encodeProcessor) Teardown(ctx context.Context) error {
	return nil
}
