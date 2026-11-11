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

package internal

import (
	"context"
	"slices"
	"strings"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/errsafe"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/toschema"
	"github.com/twmb/franz-go/pkg/sr"
)

type Encoder struct {
	registry schemaregistry.Registry
	logger   log.CtxLogger

	SchemaStrategy

	// lossless makes Encode decode what it encoded and fail unless it gets
	// the same value back. It is set for FromRecordSchemaStrategy only, so the
	// other strategies keep their existing behavior.
	lossless bool
}

// SchemaStrategy decides the schema a value is encoded with. carried is the
// schema the record part carries according to its metadata (the zero value
// when it carries none, or when the strategy does not use it).
type SchemaStrategy interface {
	GetSchema(ctx context.Context, registry schemaregistry.Registry, logger log.CtxLogger, sd opencdc.StructuredData, carried SchemaRef) (schema.Schema, error)
}

func NewEncoder(registry schemaregistry.Registry, logger log.CtxLogger, strategy SchemaStrategy) *Encoder {
	_, lossless := strategy.(*FromRecordSchemaStrategy)
	return &Encoder{
		registry:       registry,
		logger:         logger.WithComponent("avro.internal.Encoder"),
		SchemaStrategy: strategy,
		lossless:       lossless,
	}
}

// Encode encodes sd in the Confluent wire format with the schema the strategy
// returns. carried is the schema the record part carries (see SchemaStrategy).
func (e *Encoder) Encode(ctx context.Context, sd opencdc.StructuredData, carried SchemaRef) (opencdc.RawData, error) {
	s, err := e.GetSchema(ctx, e.registry, e.logger, sd, carried)
	if err != nil {
		return opencdc.RawData{}, cerrors.Errorf("failed to get schema: %w", err)
	}

	// TODO note that we need to pass in the index when adding support for protobuf
	header, err := (&sr.ConfluentHeader{}).AppendEncode(nil, s.ID, nil)
	if err != nil {
		return nil, cerrors.Errorf("failed to encode header: %w", err)
	}

	data, err := s.Marshal(sd)
	if err != nil {
		if e.lossless {
			return nil, cerrors.Errorf("failed to marshal data with schema (ID: %v, subject: %v, version: %v): %w", s.ID, s.Subject, s.Version, mismatchFromCodec(sd, err))
		}
		return nil, cerrors.Errorf("failed to marshal data with schema (ID: %v, subject: %v, version: %v): %w", s.ID, s.Subject, s.Version, errsafe.Opaque(err))
	}
	if e.lossless {
		if err := verifyRoundTrip(s, sd, data); err != nil {
			return nil, cerrors.Errorf("schema (ID: %v, subject: %v, version: %v): %w", s.ID, s.Subject, s.Version, err)
		}
	}

	return append(header, data...), nil
}

// verifyRoundTrip decodes data, the encoding of sd with s, and returns a
// CodeSchemaMismatch error unless it gives back the same value.
//
// Invariant 6: the Avro codec accepts some values it cannot represent. It
// truncates a decimal to the schema's scale and a time to the precision of
// its logical type, and ignores a map key that is not a field of a nested
// record. Checking the round trip, not trusting a successful encode, turns
// each of those into an error instead of a changed value.
func verifyRoundTrip(s schema.Schema, sd opencdc.StructuredData, data []byte) error {
	var decoded opencdc.StructuredData
	if err := s.Unmarshal(data, &decoded); err != nil {
		return mismatch("(record)", "does not decode after encoding", err)
	}
	if k := localWallClockChanged(s, sd); k != "" {
		return mismatch(k, "would not survive encoding: a local-timestamp field is written with the wall clock the time has in the local time zone, not the wall clock of the time's own zone", nil)
	}
	if len(decoded) != len(sd) {
		return mismatch(firstMissingKey(sd, decoded), "would not survive encoding", nil)
	}
	for _, k := range sortedKeys(sd) {
		if !sameValue(sd[k], decoded[k]) {
			return mismatch(k, "would not survive encoding: the schema cannot represent its value exactly", nil)
		}
	}
	return nil
}

// mismatchFromCodec turns a codec error into a CodeSchemaMismatch error. It
// names the top-level field when the codec message starts with one (the codec
// prefixes the field path, "name: avro: ..."), and only when that name is a
// key of sd; the rest of the codec message is not shown, as it can quote
// record values.
func mismatchFromCodec(sd opencdc.StructuredData, err error) error {
	field := "(record)"
	msg := err.Error()
	if i := strings.Index(msg, codecMarshalPrefix); i >= 0 {
		rest := msg[i+len(codecMarshalPrefix):]
		if j := strings.IndexAny(rest, ":.["); j > 0 {
			if _, ok := sd[rest[:j]]; ok {
				field = rest[:j]
			}
		}
	}
	return mismatch(field, "cannot be encoded with the schema: its value has a different type, is out of range, or is null where the schema does not allow it", err)
}

// codecMarshalPrefix precedes the field path in conduit-commons' Avro marshal
// errors. If a codec upgrade changes it, the error still has the right code
// and only loses the field name.
const codecMarshalPrefix = "could not marshal into avro: "

func firstMissingKey(want, got opencdc.StructuredData) string {
	for _, k := range sortedKeys(want) {
		if _, ok := got[k]; !ok {
			return k
		}
	}
	return "(record)"
}

func sortedKeys(sd opencdc.StructuredData) []string {
	keys := make([]string, 0, len(sd))
	for k := range sd {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

type ExtractAndUploadSchemaStrategy struct {
	Subject string
}

func (str ExtractAndUploadSchemaStrategy) GetSchema(ctx context.Context, registry schemaregistry.Registry, _ log.CtxLogger, sd opencdc.StructuredData, _ SchemaRef) (schema.Schema, error) {
	s, err := schema.KnownSerdeFactories[schema.TypeAvro].SerdeForType(sd)
	if err != nil {
		return schema.Schema{}, cerrors.Errorf("could not extract avro schema: %w", err)
	}

	ss, err := registry.CreateSchema(ctx, str.Subject, sr.Schema{
		Schema: s.String(),
		Type:   sr.TypeAvro,
	})
	if err != nil {
		return schema.Schema{}, cerrors.Errorf("could not create schema: %w", err)
	}

	return toschema.SrSubjectSchema(ss), nil
}

type DownloadSchemaStrategy struct {
	Subject string
	// TODO add support for specifying "latest" - https://github.com/ConduitIO/conduit/issues/1095
	Version int
}

func (str DownloadSchemaStrategy) GetSchema(ctx context.Context, registry schemaregistry.Registry, _ log.CtxLogger, _ opencdc.StructuredData, _ SchemaRef) (schema.Schema, error) {
	// get schema from registry
	ss, err := registry.SchemaBySubjectVersion(ctx, str.Subject, str.Version)
	if err != nil {
		return schema.Schema{}, cerrors.Errorf("could not get schema with subject %q and version %q: %w", str.Subject, str.Version, err)
	}

	return toschema.SrSubjectSchema(ss), nil
}
