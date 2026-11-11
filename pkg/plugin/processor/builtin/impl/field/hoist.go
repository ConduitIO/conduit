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

//go:generate paramgen -output=hoist_paramgen.go hoistConfig

package field

import (
	"bytes"
	"context"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// HoistProcessor is the `field.hoist` built-in processor, the equivalent of
// Kafka Connect's HoistField$Key and HoistField$Value SMTs. It replaces a
// whole key, before-image or after-image with structured data holding the
// old side under one field name:
//
//   - structured data is deep-copied into the new map (as a map[string]any);
//   - raw data becomes a []byte value, as Kafka Connect wraps a byte array;
//   - a nil key or after-image becomes {name: nil}, as Kafka Connect wraps a
//     null value. This turns a tombstone into a record with a value, exactly
//     as the SMT does;
//   - a nil before-image passes through unchanged, schema metadata included:
//     it means "no image" (an insert), not a null value, and Kafka Connect
//     has no before-image to wrap.
//
// A typed nil StructuredData counts as nil.
//
// It removes the schema subject and version of the side it hoists, so the
// SDK schema middleware does not register a schema extracted from the new
// shape as a new version of the source's subject (invariant 6).
type HoistProcessor struct {
	sdk.UnimplementedProcessor

	config hoistConfig
}

// NewHoistProcessor returns an unconfigured `field.hoist` processor.
func NewHoistProcessor(log.CtxLogger) *HoistProcessor {
	return &HoistProcessor{}
}

type hoistConfig struct {
	// Field is the side of the record to hoist: `.Key`, `.Payload.Before` or
	// `.Payload.After`. The default `.Payload.After` matches Kafka Connect's
	// `HoistField$Value`; `.Key` matches `HoistField$Key`.
	Field string `json:"field" default:".Payload.After" validate:"inclusion=.Key|.Payload.Before|.Payload.After"`
	// Name is the field name the old key or payload is stored under. It is
	// used as written: a `.` in it is part of the name, not a path.
	Name string `json:"name" validate:"required"`
}

func (p *HoistProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "field.hoist",
		Summary: "Wrap the key or payload in a structure under one field name.",
		Description: `Hoist replaces the whole ` + "`field`" + ` (` + "`.Key`" + `, ` + "`.Payload.Before`" + ` or ` + "`.Payload.After`" + `)
with structured data that holds the old value under ` + "`name`" + `: ` + "`{\"id\": 1}`" + ` becomes
` + "`{\"<name>\": {\"id\": 1}}`" + `. It is the equivalent of the Kafka Connect ` + "`HoistField$Value`" + ` and
` + "`HoistField$Key`" + ` single message transforms.

Structured data is deep-copied. Raw data is stored as bytes. A nil key or after-image becomes
` + "`{\"<name>\": null}`" + `, as Kafka Connect wraps a null value: a tombstone or a delete's after-image therefore gets a
value. Use a ` + "`condition`" + ` to skip such records if that is not wanted. A nil before-image means the record has no
before-image (an insert), so it passes through unchanged.

The processor removes the schema subject and version of the side it hoists (` + "`opencdc.key.schema.*`" + ` for the key,
` + "`opencdc.payload.schema.*`" + ` for the payload), because the new shape no longer matches the schema the source
attached. The record then leaves the processor as structured data without a schema.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: hoistConfig{}.Parameters(),
	}, nil
}

func (p *HoistProcessor) Configure(ctx context.Context, c config.Config) error {
	cfg := hoistConfig{}
	if err := sdk.ParseConfig(ctx, c, &cfg, hoistConfig{}.Parameters()); err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}
	p.config = cfg
	return nil
}

func (p *HoistProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		var side *opencdc.Data
		switch p.config.Field {
		case ".Key":
			side = &rec.Key
		case ".Payload.Before":
			side = &rec.Payload.Before
		default:
			side = &rec.Payload.After
		}
		if isNilData(*side) && p.config.Field == ".Payload.Before" {
			// No before-image: nothing to wrap, and the metadata still
			// describes the after-image.
			out = append(out, sdk.SingleRecord(rec))
			continue
		}
		*side = opencdc.StructuredData{p.config.Name: hoistedValue(*side)}

		// Invariant 6: the new shape no longer matches the schema the source
		// attached; without these keys the SDK schema middleware leaves it
		// structured instead of registering a lossy extracted schema.
		if p.config.Field == ".Key" {
			delete(rec.Metadata, opencdc.MetadataKeySchemaSubject)
			delete(rec.Metadata, opencdc.MetadataKeySchemaVersion)
		} else {
			delete(rec.Metadata, opencdc.MetadataPayloadSchemaSubject)
			delete(rec.Metadata, opencdc.MetadataPayloadSchemaVersion)
		}
		out = append(out, sdk.SingleRecord(rec))
	}
	return out
}

// hoistedValue returns the value a side is stored as under the hoisted
// name: a deep copy of structured data as a map[string]any (the type nested
// maps have in decoded records), the bytes of raw data, or nil.
func hoistedValue(d opencdc.Data) any {
	if isNilData(d) {
		return nil
	}
	switch d := d.(type) {
	case opencdc.StructuredData:
		return map[string]any(d.Clone().(opencdc.StructuredData)) //nolint:forcetypeassert // Clone keeps the type
	default:
		// RawData, or any other Data implementation: its bytes.
		return bytes.Clone(d.Bytes())
	}
}

// isNilData reports whether d holds no data: a nil interface or a typed nil
// StructuredData. RawData(nil) is bytes (an empty value), not nil.
func isNilData(d opencdc.Data) bool {
	if d == nil {
		return true
	}
	sd, ok := d.(opencdc.StructuredData)
	return ok && sd == nil
}
