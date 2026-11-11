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

//go:generate paramgen -output=set_paramgen.go setConfig

package field

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/tplerr"
)

// SetProcessor is the `field.set` built-in processor. It sets one field of a
// record to the rendered output of a Go template (`value`), optionally
// converted to a type (`type`), or to a typed deep copy of another field
// (`from`).
//
// With only `field` and `value` set it behaves exactly as v0.1.0 did: the
// template output is stored as a string. A test pins that against a frozen
// copy of the v0.1.0 implementation.
type SetProcessor struct {
	referenceResolver sdk.ReferenceResolver
	tmpl              *template.Template

	// fromFields is the path of `from`, set when the processor copies a
	// field. It is read with lookup, which never changes the record.
	fromFields []string
	// typ is the conversion target of the template output. Empty means
	// string, the v0.1.0 behaviour.
	typ string
	// side is the record side whose schema metadata a typed set or a copy
	// removes (see dropSchemaMetadata). sideNone for the v0.1.0 behaviour.
	side schemaSide
	// wholeSide reports whether field is a whole side (.Key,
	// .Payload.Before or .Payload.After) rather than a field inside one.
	wholeSide bool
	cfg       setConfig

	sdk.UnimplementedProcessor
}

// NewSetProcessor returns an unconfigured `field.set` processor.
func NewSetProcessor(log.CtxLogger) *SetProcessor {
	return &SetProcessor{}
}

type setConfig struct {
	// Field is the target field that will be set. Note that it is not allowed
	// to set the `.Position` field.
	//
	// For more information about the format, see [Referencing fields](https://conduitdata.io/docs/using/processors/referencing-fields).
	Field string `json:"field" validate:"required,exclusion=.Position"`
	// Value is a Go template expression which will be evaluated and stored in `field` (e.g. `{{ .Payload.After }}`).
	// Exactly one of `value` and `from` must be set.
	Value string `json:"value"`
	// From is a reference to a field whose value is copied into `field`, with
	// its type kept (e.g. `.Payload.After.id`). Nested data is deep-copied, so
	// the two fields never share data. A field that holds null is copied as
	// null; a missing field fails the record. It is a reference, not a
	// template.
	// Exactly one of `value` and `from` must be set.
	//
	// For more information about the format, see [Referencing fields](https://conduitdata.io/docs/using/processors/referencing-fields).
	From string `json:"from"`
	// Type is the type the output of the `value` template is converted to
	// before it is stored: `string`, `int`, `float`, `bool` or `time`. The
	// conversion is the one `field.convert` uses. It cannot be combined with
	// `from`, which keeps the type of the copied field.
	Type string `json:"type" default:"string" validate:"inclusion=string|int|float|bool|time"`
}

func (p *SetProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "field.set",
		Summary: "Set the value of a certain field.",
		Description: `Set the value of a certain field to any value. It is not allowed to set the ` + "`.Position`" + ` field.
The new value can be a Go template expression, the processor will evaluate the output and assign the value to the target field.
If the provided ` + "`field`" + ` doesn't exist, the processor will create that field and assign its value.
This processor can be used for multiple purposes, like extracting fields, hoisting data, inserting fields, copying fields, masking fields, etc.
Note that this processor only runs on structured data, if the record contains raw JSON data, then use the processor
[` + "`json.decode`" + `](/docs/using/processors/builtin/json.decode) to parse it into structured data first.

The output of the ` + "`value`" + ` template is a string. Set ` + "`type`" + ` to convert it to an ` + "`int`" + `, ` + "`float`" + `,
` + "`bool`" + ` or ` + "`time`" + ` before it is stored, with the same conversion as
[` + "`field.convert`" + `](/docs/using/processors/builtin/field.convert). To copy a field with its type kept, set
` + "`from`" + ` to a field reference instead of ` + "`value`" + `; nested data is deep-copied. Exactly one of ` + "`value`" + `
and ` + "`from`" + ` must be set. A field that holds null is copied as null; when ` + "`field`" + ` is a whole side, the side
becomes nil, never an empty map. A field that does not exist fails the record with ` + "`field.set.from_missing`" + `, and
reading it creates nothing in the record.

With ` + "`from`" + ` or ` + "`type`" + `, a whole ` + "`.Key`" + `, ` + "`.Payload.Before`" + ` or ` + "`.Payload.After`" + ` can only be set
to structured data or null, and a field can only be read from or written into a side that holds structured data
(or nil, when writing). Anything else fails the record with ` + "`field.set.side_not_structured`" + `; a value is never
converted to fit.

When ` + "`from`" + ` or a ` + "`type`" + ` other than ` + "`string`" + ` changes a field under ` + "`.Key`" + ` or ` + "`.Payload`" + `, the
processor removes the schema subject and version of that side (` + "`opencdc.key.schema.*`" + ` or
` + "`opencdc.payload.schema.*`" + `), because the side no longer matches the schema the source attached. The record then
leaves the processor as structured data without a schema, instead of being encoded with a schema extracted from its
values. With only ` + "`value`" + ` set, the metadata is kept, as in earlier versions.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: setConfig{}.Parameters(),
	}, nil
}

func (p *SetProcessor) Configure(ctx context.Context, c config.Config) error {
	// Read before ParseConfig, which fills in the default of "type".
	_, typeSet := c[setConfigType]
	cfg := setConfig{}
	err := sdk.ParseConfig(ctx, c, &cfg, setConfig{}.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}

	switch {
	case cfg.Value != "" && cfg.From != "":
		return newSetConfigError(CodeSetValueAndFrom,
			`"value" and "from" are both set`, setConfigFrom,
			`Set "value" to render a template, or "from" to copy a field, not both.`)
	case cfg.Value == "" && cfg.From == "":
		return newSetConfigError(CodeSetValueRequired,
			`one of "value" and "from" is required`, setConfigValue,
			`Set "value" to a Go template, or "from" to the field to copy.`)
	case cfg.From != "" && typeSet:
		return newSetConfigError(CodeSetTypeWithFrom,
			`"type" cannot be combined with "from"`, setConfigType,
			`Remove "type": "from" keeps the type of the copied field. Use "value" with "type" to convert a template.`)
	}

	// Reset everything a previous Configure set.
	*p = SetProcessor{cfg: cfg}

	resolver, err := sdk.NewReferenceResolver(cfg.Field)
	if err != nil {
		return cerrors.Errorf("failed to parse the %q param: %w", "field", err)
	}
	p.referenceResolver = resolver

	if cfg.From != "" {
		_, err := sdk.NewReferenceResolver(cfg.From)
		if err == nil {
			// The whole record or the whole payload (before and after
			// together) is not a value a field can hold.
			if f := referenceFields(cfg.From); len(f) == 0 || (len(f) == 1 && f[0] == "Payload") {
				err = cerrors.New(`"from" must name a field, .Key, .Payload.Before or .Payload.After, not the whole record or payload`)
			}
		}
		if err != nil {
			// A template ("{{ .Payload.After.id }}") lands here: from is a
			// reference, never a template.
			e := conduiterr.Wrap(CodeSetFromInvalid, fmt.Sprintf("%q is not a valid field reference", setConfigFrom), err)
			e.ConfigPath = "settings." + setConfigFrom
			e.Suggestion = `Set "from" to a field reference such as .Payload.After.id, without {{ }}.`
			return e
		}
		p.fromFields = referenceFields(cfg.From)
		p.side = schemaSideOf(cfg.Field)
		p.wholeSide = isWholeSide(cfg.Field)
		return nil
	}

	tmpl, err := template.New("").Funcs(sprig.FuncMap()).Parse(cfg.Value)
	if err != nil {
		return cerrors.Errorf("failed to parse the %q param template: %w", "value", err)
	}
	p.tmpl = tmpl
	if cfg.Type != "string" {
		p.typ = cfg.Type
		p.side = schemaSideOf(cfg.Field)
		p.wholeSide = isWholeSide(cfg.Field)
	}
	return nil
}

func (p *SetProcessor) Process(_ context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	if p.fromFields == nil && p.typ == "" {
		return p.processTemplate(records)
	}
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		rec, err := p.processTyped(record)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, sdk.SingleRecord(rec))
	}
	return out
}

// processTemplate is the v0.1.0 behaviour: the template output is stored as a
// string, and errors are returned as they were. TestSetField_DefaultIsUnchanged
// compares it with a frozen copy of v0.1.0; change neither without the other.
func (p *SetProcessor) processTemplate(records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		rec := record
		var b bytes.Buffer
		// evaluate the new value
		err := p.tmpl.Execute(&b, rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: tplerr.Exec(err)})
		}
		ref, err := p.referenceResolver.Resolve(&rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		err = ref.Set(b.String())
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, sdk.SingleRecord(rec))
	}
	return out
}

// processTyped sets the field to a typed value: a copy of `from`, or the
// template output converted to `type`.
func (p *SetProcessor) processTyped(rec opencdc.Record) (opencdc.Record, error) {
	var value any
	if p.fromFields != nil {
		v, err := p.readFrom(rec)
		if err != nil {
			return opencdc.Record{}, err
		}
		value = v
	} else {
		var b bytes.Buffer
		if err := p.tmpl.Execute(&b, rec); err != nil {
			return opencdc.Record{}, tplerr.Exec(err)
		}
		v, err := stringToType(b.String(), p.typ)
		if err != nil {
			// The cause is sanitized by stringToType: it never quotes the value.
			e := conduiterr.Wrap(CodeSetTypeConversion,
				fmt.Sprintf("the output of %q for field %s could not be converted to %s", setConfigValue, p.cfg.Field, p.typ), err)
			e.ConfigPath = "settings." + setConfigType
			e.Suggestion = fmt.Sprintf(`Make the "value" template render a valid %s, or route such records with a condition.`, p.typ)
			return opencdc.Record{}, e
		}
		value = v
	}

	if p.wholeSide {
		switch v := value.(type) {
		case nil, opencdc.StructuredData:
		case map[string]any:
			value = opencdc.StructuredData(v)
		default:
			// Invariant 6: a side written by a copy or a typed set holds
			// structured data or nothing. Anything else (a string would
			// become raw bytes, written as base64 in JSON) is refused,
			// never converted.
			e := conduiterr.New(CodeSetSideNotStructured,
				fmt.Sprintf("%s can only be set to structured data or null, not a value of type %T", p.cfg.Field, value))
			e.ConfigPath = "settings." + setConfigField
			e.Suggestion = "Set a field inside the side (for example .Payload.After.value) instead of the whole side."
			return opencdc.Record{}, e
		}
	}

	ref, err := p.referenceResolver.Resolve(&rec)
	if err != nil {
		// The target path enters a side holding raw data (or a parent that
		// is not a map): there is no field to set.
		e := conduiterr.Wrap(CodeSetSideNotStructured,
			fmt.Sprintf("field %s cannot be set: its parent does not contain structured data", p.cfg.Field), err)
		e.ConfigPath = "settings." + setConfigField
		e.Suggestion = "Decode raw data first (for example with json.decode), or set a field outside it."
		return opencdc.Record{}, e
	}
	if err := ref.Set(value); err != nil {
		// Invariant 6: a value the target cannot hold (an int into .Key, a
		// map into .Metadata.x) fails the record; it is never coerced.
		e := conduiterr.Wrap(CodeSetValueNotSettable,
			fmt.Sprintf("field %s cannot hold a value of type %T", p.cfg.Field, value), err)
		e.ConfigPath = "settings." + setConfigField
		e.Suggestion = "Set a field that can hold this type: a structured field, or .Key/.Payload.* with structured data, a string or bytes."
		return opencdc.Record{}, e
	}
	// Invariant 6: the side no longer matches the schema the source attached.
	dropSchemaMetadata(rec.Metadata, p.side)
	return rec, nil
}

// readFrom returns a deep copy of the value of the `from` field. It never
// changes rec, and the copy shares no data with it. A field that holds null
// gives nil; a missing field is an error, as in Kafka Connect.
func (p *SetProcessor) readFrom(rec opencdc.Record) (any, error) {
	v, res := lookup(rec, p.fromFields)
	switch res {
	case lookupFound:
		return deepCopy(v), nil
	case lookupRawSide:
		e := conduiterr.New(CodeSetSideNotStructured,
			fmt.Sprintf("field %s named by %q cannot be read: its side holds raw data", p.cfg.From, setConfigFrom))
		e.ConfigPath = "settings." + setConfigFrom
		e.Suggestion = "Decode raw data first, for example with json.decode."
		return nil, e
	case lookupMissing:
		fallthrough
	default:
		e := conduiterr.New(CodeSetFromMissing,
			fmt.Sprintf("field %s named by %q does not exist in this record", p.cfg.From, setConfigFrom))
		e.ConfigPath = "settings." + setConfigFrom
		e.Suggestion = "Route records without the field elsewhere with a condition, or copy a field every record has."
		return nil, e
	}
}

// schemaSide is the side of a record whose schema metadata a processor
// removes after reshaping it.
type schemaSide int

const (
	schemaSideNone schemaSide = iota
	schemaSideKey
	schemaSidePayload
)

// schemaSideOf returns the side a valid field reference points into. It
// accepts the spellings the reference resolver accepts (`.Key`, `.key`,
// `$.Key`, and the same for `.Payload`); a lower-case spelling after `$` is not a valid reference.
func schemaSideOf(ref string) schemaSide {
	ref = strings.TrimPrefix(ref, "$")
	for _, s := range []struct {
		prefix string
		side   schemaSide
	}{
		{".Key", schemaSideKey},
		{".key", schemaSideKey},
		{".Payload", schemaSidePayload},
		{".payload", schemaSidePayload},
	} {
		if rest, ok := strings.CutPrefix(ref, s.prefix); ok && (rest == "" || rest[0] == '.' || rest[0] == '[') {
			return s.side
		}
	}
	return schemaSideNone
}

// isWholeSide reports whether a valid field reference names a whole side:
// .Key, .Payload.Before or .Payload.After, in any spelling the reference
// resolver accepts.
func isWholeSide(ref string) bool {
	switch strings.ToLower(strings.TrimPrefix(ref, "$")) {
	case ".key", ".payload.before", ".payload.after":
		return true
	}
	return false
}

// dropSchemaMetadata removes the schema subject and version of side, so the
// SDK schema encode middleware leaves the reshaped side structured instead of
// registering a schema extracted from its values (which widens int to long and
// drops logical types) as a new version of the source's subject.
func dropSchemaMetadata(md opencdc.Metadata, side schemaSide) {
	switch side {
	case schemaSideKey:
		delete(md, opencdc.MetadataKeySchemaSubject)
		delete(md, opencdc.MetadataKeySchemaVersion)
	case schemaSidePayload:
		delete(md, opencdc.MetadataPayloadSchemaSubject)
		delete(md, opencdc.MetadataPayloadSchemaVersion)
	case schemaSideNone:
	}
}
