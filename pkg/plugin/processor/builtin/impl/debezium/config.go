// Copyright © 2026 Meroxa, Inc.
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

//go:generate paramgen -output=config_paramgen.go encodeConfig

package debezium

import (
	"context"

	"github.com/conduitio/conduit-commons/config"
	sdk "github.com/conduitio/conduit-processor-sdk"
)

// Compat values, matching the Postgres source's debezium.compat boundaries.
const (
	Compat26 = "2.6"
	Compat35 = "3.5"
)

// Converter names.
const (
	converterJSON = "json"
	converterAvro = "avro"
)

// Decimal formats of the JsonConverter (json.decimal.format).
const (
	decimalBase64  = "BASE64"
	decimalNumeric = "NUMERIC"
)

// config is the debezium.encode processor configuration.
type encodeConfig struct {
	// KeyConverter selects the key converter ("json" or "avro").
	//
	// The Debezium-compatible output design (#3033) makes "avro" the default;
	// this revision implements the JsonConverter first and defaults to "json"
	// until the Confluent AvroConverter port lands.
	KeyConverter string `json:"key.converter" default:"json" validate:"inclusion=json|avro"`
	// ValueConverter selects the value converter ("json" or "avro").
	ValueConverter string `json:"value.converter" default:"json" validate:"inclusion=json|avro"`
	// KeySchemasEnable mirrors key.converter.schemas.enable: when true the
	// JsonConverter includes the schema next to the key payload.
	KeySchemasEnable bool `json:"key.json.schemas.enable" default:"true"`
	// ValueSchemasEnable mirrors value.converter.schemas.enable: when true the
	// JsonConverter includes the schema next to the value payload.
	ValueSchemasEnable bool `json:"value.json.schemas.enable" default:"true"`
	// DecimalFormat mirrors decimal.format: "BASE64" writes the unscaled
	// decimal as base64, "NUMERIC" as a JSON number.
	DecimalFormat string `json:"json.decimal.format" default:"BASE64" validate:"inclusion=BASE64|NUMERIC"`
	// TombstonesOnDelete mirrors tombstones.on.delete: a delete expands into a
	// delete event followed by a tombstone (same key, null value).
	TombstonesOnDelete bool `json:"tombstones.on.delete" default:"true"`
	// Compat selects the Debezium source-schema shape (the snapshot enum's
	// allowed list, the source schema .version, and the origin/origin_lsn
	// fields). It must match the source's debezium.compat.
	Compat string `json:"debezium.compat" default:"3.5" validate:"inclusion=2.6|3.5"`
}

func parseConfig(ctx context.Context, c config.Config) (encodeConfig, error) {
	cfg := encodeConfig{}
	err := sdk.ParseConfig(ctx, c, &cfg, cfg.Parameters())
	if err != nil {
		return encodeConfig{}, err
	}
	return cfg, nil
}
