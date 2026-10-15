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

package debezium

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/impl/debezium/connect"
)

// Metadata keys of the record shape between the source and the encoder
// (design: Record shape between source and processor).
const (
	MetaTopic      = "debezium.topic"
	MetaOp         = "debezium.op"
	MetaDescriptor = "debezium.descriptor"
	MetaOldKey     = "debezium.old_key" // schemaless JSON payload, NUMERIC decimals
	MetaSourcePfx  = "debezium.source."
	// MetaTxID, MetaTxTotal and MetaTxCollOrder fill the envelope's
	// transaction block (provide.transaction.metadata). The id is
	// "<txId>:<lsn>" with the change's own LSN, per event (golden
	// json-txmeta). The orders are those of the plan's first event; a
	// primary-key change is two events, so the source advances its counters
	// by two for it.
	MetaTxID        = "debezium.tx.id"
	MetaTxTotal     = "debezium.tx.total_order"
	MetaTxCollOrder = "debezium.tx.data_collection_order"
)

// Header names Debezium writes.
const (
	HeaderNewKey       = "__debezium.newkey"
	HeaderOldKey       = "__debezium.oldkey"
	headerContextPfx   = "__debezium.context."
	connectorNamePgSQL = "postgresql"
)

// Options configures a Builder. They mirror Debezium and converter settings;
// see the design's Settings tables.
type Options struct {
	// Compat is debezium.compat.
	Compat Compat
	// TombstonesOnDelete is tombstones.on.delete (Debezium default true).
	TombstonesOnDelete bool
	// ExtendedHeaders is extended.headers.enabled: add the
	// __debezium.context.* headers to every message (Debezium's default from
	// 3.3).
	ExtendedHeaders bool
	// RunID is the __debezium.context.runId header value. Debezium generates
	// one per connector start; it is volatile and never compared byte for
	// byte.
	RunID string
	// Now is the clock for the envelope's ts_ms/ts_us/ts_ns, which Debezium
	// sets when it builds the event (design D1). Nil means time.Now.
	Now func() time.Time
}

// Builder turns source records into message plans. It is safe for concurrent
// use.
type Builder struct {
	opts         Options
	srcSchema    *connect.Schema
	msgValSchema *connect.Schema

	mu          sync.Mutex
	descriptors map[string]*shape // keyed by the descriptor text
}

// shape is everything derived from one descriptor.
type shape struct {
	desc     *Descriptor
	envelope *connect.Schema
}

// maxCachedShapes bounds the descriptor cache. Real pipelines have a handful
// of shapes per table; a source that sends a new descriptor per record still
// works, it just stops benefiting from the cache.
const maxCachedShapes = 4096

// NewBuilder validates the options and returns a Builder.
func NewBuilder(opts Options) (*Builder, error) {
	if _, err := ParseCompat(string(opts.Compat)); err != nil {
		return nil, err
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Builder{
		opts:         opts,
		srcSchema:    sourceSchema(opts.Compat),
		msgValSchema: messageValueSchema(opts.Compat),
		descriptors:  map[string]*shape{},
	}, nil
}

// Message is one Kafka message of a plan, as Connect data. A nil KeySchema is
// a null key; a nil ValueSchema with a nil Value is a tombstone (null value).
type Message struct {
	Topic       string
	KeySchema   *connect.Schema
	Key         any
	ValueSchema *connect.Schema
	Value       any
	Headers     []Header
}

// Header is one Kafka header as Connect data.
type Header struct {
	Key    string
	Schema *connect.Schema
	Value  any
}

// Plan returns the messages Debezium produces for the WAL change the record
// describes, in Debezium's order. The record is not modified.
//
//nolint:gocyclo // one branch per op; each mirrors a Debezium emitter path
func (b *Builder) Plan(rec opencdc.Record) ([]Message, error) {
	op := rec.Metadata[MetaOp]
	topic := rec.Metadata[MetaTopic]
	if topic == "" {
		return nil, mismatch(MetaTopic, "missing")
	}
	if op == "m" {
		m, err := b.logicalMessage(rec, topic)
		if err != nil {
			return nil, err
		}
		return []Message{m}, nil
	}

	sh, err := b.shape(rec.Metadata[MetaDescriptor])
	if err != nil {
		return nil, err
	}
	key, err := b.keyValue(sh, rec.Key, "$.Key")
	if err != nil {
		return nil, err
	}
	source, err := b.sourceValue(rec.Metadata)
	if err != nil {
		return nil, err
	}
	tx, err := txValue(rec.Metadata)
	if err != nil {
		return nil, err
	}

	switch op {
	case "c", "r", "u":
		if op == "u" {
			if oldKeyText, ok := rec.Metadata[MetaOldKey]; ok {
				return b.primaryKeyChange(sh, rec, topic, key, oldKeyText, source, tx)
			}
		}
		var before any
		if op == "u" {
			if before, err = rowValue(sh, rec.Payload.Before, "$.Payload.Before"); err != nil {
				return nil, err
			}
		} else if !isEmpty(rec.Payload.Before) {
			return nil, mismatch("$.Payload.Before", fmt.Sprintf("must be empty for op %q", op))
		}
		after, err := rowValue(sh, rec.Payload.After, "$.Payload.After")
		if err != nil {
			return nil, err
		}
		if after == nil {
			return nil, mismatch("$.Payload.After", fmt.Sprintf("required for op %q", op))
		}
		return []Message{b.rowMessage(sh, topic, key, before, after, op, source, tx, nil)}, nil
	case "d":
		before, err := rowValue(sh, rec.Payload.Before, "$.Payload.Before")
		if err != nil {
			return nil, err
		}
		if !isEmpty(rec.Payload.After) {
			return nil, mismatch("$.Payload.After", `must be empty for op "d"`)
		}
		// Invariant 4: the tombstone follows its delete in the same plan and
		// carries the same key, so both land on one partition in order.
		msgs := []Message{b.rowMessage(sh, topic, key, before, nil, op, source, tx, nil)}
		if b.opts.TombstonesOnDelete {
			msgs = append(msgs, b.tombstone(sh, topic, key, source, nil))
		}
		return msgs, nil
	case "t":
		return nil, conduiterr.New(connect.CodeUnsupportedType,
			"truncate events are not implemented yet: no golden capture exists (Debezium skips truncates by default, skipped.operations=t)")
	case "":
		return nil, mismatch(MetaOp, "missing")
	}
	return nil, mismatch(MetaOp, fmt.Sprintf("unknown op %q", op))
}

// primaryKeyChange is RelationalChangeRecordEmitter.emitUpdateAsPrimaryKeyChangeRecord.
// The old key arrives in debezium.old_key as the key's schemaless JSON
// payload with decimals as JSON numbers (decimal.format=NUMERIC), which keeps
// a numeric key's scale exactly ("1.50" stays 1.50). It is parsed strictly
// (ParsePayloadJSON): a duplicate member or an altered character would make
// the delete target a key that is not the old one (invariant 6).
//
// a delete for the old key with header newkey, its tombstone with the same
// header, then a create for the new key with header oldkey. The delete's
// "before" is whatever the source put in Payload.Before for the old row.
func (b *Builder) primaryKeyChange(sh *shape, rec opencdc.Record, topic string, newKey any, oldKeyText string, source, tx *connect.Struct) ([]Message, error) {
	if sh.desc.Key == nil {
		return nil, mismatch(MetaOldKey, "set on a table without a key")
	}
	oldGo, err := connect.ParsePayloadJSON(sh.desc.Key, []byte(oldKeyText), connect.DecimalNumeric)
	if err != nil {
		return nil, mismatch(MetaOldKey, err.Error())
	}
	oldKey, err := connect.FromGo(sh.desc.Key, oldGo)
	if err != nil {
		return nil, err
	}
	before, err := rowValue(sh, rec.Payload.Before, "$.Payload.Before")
	if err != nil {
		return nil, err
	}
	after, err := rowValue(sh, rec.Payload.After, "$.Payload.After")
	if err != nil {
		return nil, err
	}
	if after == nil {
		return nil, mismatch("$.Payload.After", `required for op "u"`)
	}
	newKeyHdr := []Header{{Key: HeaderNewKey, Schema: sh.desc.Key, Value: newKey}}
	oldKeyHdr := []Header{{Key: HeaderOldKey, Schema: sh.desc.Key, Value: oldKey}}
	msgs := []Message{b.rowMessage(sh, topic, oldKey, before, nil, "d", source, tx, newKeyHdr)}
	if b.opts.TombstonesOnDelete {
		msgs = append(msgs, b.tombstone(sh, topic, oldKey, source, newKeyHdr))
	}
	// Debezium numbers every event it emits within the transaction, so the
	// create is one past the delete in both orders (golden json-txmeta; the
	// tombstone is not an event). The source must therefore count a
	// primary-key change as two events when it numbers later changes of the
	// same transaction.
	msgs = append(msgs, b.rowMessage(sh, topic, newKey, nil, after, "c", source, nextEvent(tx), oldKeyHdr))
	return msgs, nil
}

func (b *Builder) rowMessage(sh *shape, topic string, key, before, after any, op string, source, tx *connect.Struct, hdrs []Header) Message {
	now := b.opts.Now()
	ns := now.UnixNano()
	env := &connect.Struct{Schema: sh.envelope, Values: []any{
		before, after, source, txOrNil(tx), op, ns / 1e6, ns / 1e3, ns,
	}}
	return Message{
		Topic: topic, KeySchema: sh.desc.Key, Key: key,
		ValueSchema: sh.envelope, Value: env,
		Headers: b.withContext(hdrs, source),
	}
}

// tombstone is a null-value message for key. Its headers carry the source's
// logical name, so it takes the same source block as the delete it follows.
func (b *Builder) tombstone(sh *shape, topic string, key any, source *connect.Struct, hdrs []Header) Message {
	return Message{Topic: topic, KeySchema: sh.desc.Key, Key: key, Headers: b.withContext(hdrs, source)}
}

// withContext appends the __debezium.context.* headers after the key headers,
// in the order the 3.7 golden shows. connectorLogicalName is topic.prefix,
// which is source.name.
func (b *Builder) withContext(hdrs []Header, source *connect.Struct) []Header {
	if !b.opts.ExtendedHeaders {
		return hdrs
	}
	_, idx := source.Schema.Field("name")
	logicalName := source.Values[idx]
	str := &connect.Schema{Type: connect.TypeString}
	out := append([]Header{}, hdrs...)
	return append(
		out,
		Header{Key: headerContextPfx + "connectorLogicalName", Schema: str, Value: logicalName},
		Header{Key: headerContextPfx + "taskId", Schema: str, Value: "0"},
		Header{Key: headerContextPfx + "connectorName", Schema: str, Value: connectorNamePgSQL},
		Header{Key: headerContextPfx + "runId", Schema: str, Value: b.opts.RunID},
	)
}

// nextEvent is the transaction block of the event after tx in the same
// transaction and table: both orders advance by one.
func nextEvent(tx *connect.Struct) *connect.Struct {
	if tx == nil {
		return nil
	}
	return &connect.Struct{Schema: tx.Schema, Values: []any{tx.Values[0], tx.Values[1].(int64) + 1, tx.Values[2].(int64) + 1}}
}

func txOrNil(tx *connect.Struct) any {
	if tx == nil {
		return nil
	}
	return tx
}

func (b *Builder) logicalMessage(rec opencdc.Record, topic string) (Message, error) {
	key, err := connect.FromGo(messageKeySchema, structuredOrNil(rec.Key))
	if err != nil {
		return Message{}, err
	}
	source, err := b.sourceValue(rec.Metadata)
	if err != nil {
		return Message{}, err
	}
	tx, err := txValue(rec.Metadata)
	if err != nil {
		return Message{}, err
	}
	var prefix any
	if k, ok := key.(*connect.Struct); ok {
		prefix = k.Values[0]
	}
	var content any
	if rec.Payload.After != nil {
		raw, ok := rec.Payload.After.(opencdc.RawData)
		if !ok {
			return Message{}, mismatch("$.Payload.After", "a logical message's content must be raw data")
		}
		content = []byte(raw)
	}
	block := &connect.Struct{Schema: messageBlockSchema, Values: []any{prefix, content}}
	ns := b.opts.Now().UnixNano()
	values := []any{"m", ns / 1e6, source}
	if b.opts.Compat == Compat35 {
		values = append(values, txOrNil(tx))
	}
	values = append(values, block)
	return Message{
		Topic: topic, KeySchema: messageKeySchema, Key: key,
		ValueSchema: b.msgValSchema, Value: &connect.Struct{Schema: b.msgValSchema, Values: values},
		Headers: b.withContext(nil, source),
	}, nil
}

func (b *Builder) shape(text string) (*shape, error) {
	if text == "" {
		return nil, conduiterr.New(connect.CodeDescriptorMissing, "record has no "+MetaDescriptor+" metadata")
	}
	b.mu.Lock()
	sh, ok := b.descriptors[text]
	b.mu.Unlock()
	if ok {
		return sh, nil
	}
	desc, err := ParseDescriptor([]byte(text))
	if err != nil {
		return nil, err
	}
	base, ok := strings.CutSuffix(desc.Value.Name, ".Value")
	if !ok {
		return nil, conduiterr.New(connect.CodeDescriptorMissing, fmt.Sprintf("value schema name %q does not end in .Value", desc.Value.Name))
	}
	sh = &shape{desc: desc, envelope: envelopeSchema(base, desc.Value, b.opts.Compat)}
	b.mu.Lock()
	if len(b.descriptors) >= maxCachedShapes {
		b.descriptors = map[string]*shape{}
	}
	b.descriptors[text] = sh
	b.mu.Unlock()
	return sh, nil
}

func (b *Builder) keyValue(sh *shape, key opencdc.Data, path string) (any, error) {
	if sh.desc.Key == nil {
		if !isEmpty(key) {
			return nil, mismatch(path, "the descriptor has no key schema, but the record has a key")
		}
		return nil, nil
	}
	sd, ok := key.(opencdc.StructuredData)
	if !ok {
		return nil, mismatch(path, fmt.Sprintf("key must be structured data, got %T", key))
	}
	return connect.FromGo(sh.desc.Key, map[string]any(sd))
}

func rowValue(sh *shape, d opencdc.Data, path string) (any, error) {
	if isEmpty(d) {
		return nil, nil
	}
	sd, ok := d.(opencdc.StructuredData)
	if !ok {
		return nil, mismatch(path, fmt.Sprintf("row must be structured data, got %T", d))
	}
	v, err := connect.FromGo(sh.desc.Value, map[string]any(sd))
	if err != nil {
		return nil, err
	}
	return v, nil
}

func structuredOrNil(d opencdc.Data) any {
	if sd, ok := d.(opencdc.StructuredData); ok {
		return map[string]any(sd)
	}
	return nil
}

func isEmpty(d opencdc.Data) bool {
	if d == nil {
		return true
	}
	switch v := d.(type) {
	case opencdc.StructuredData:
		return v == nil
	case opencdc.RawData:
		return v == nil
	}
	return false
}

// sourceValue builds the source block from debezium.source.* metadata. Every
// field of the compat level's source schema is read from
// debezium.source.<field>; a required field that is missing, a value that
// does not parse as the field's type, or a debezium.source.* key the schema
// does not have fails the record (invariant 6: nothing is dropped).
func (b *Builder) sourceValue(md opencdc.Metadata) (*connect.Struct, error) {
	s := b.srcSchema
	out := &connect.Struct{Schema: s, Values: make([]any, len(s.Fields))}
	for i, f := range s.Fields {
		raw, ok := md[MetaSourcePfx+f.Name]
		if !ok {
			if !f.Schema.Optional {
				return nil, mismatch(MetaSourcePfx+f.Name, "missing")
			}
			continue
		}
		switch f.Schema.Type { //nolint:exhaustive // the remaining types share the default branch
		case connect.TypeString:
			out.Values[i] = raw
		case connect.TypeInt64:
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return nil, mismatch(MetaSourcePfx+f.Name, fmt.Sprintf("%q is not an int64", raw))
			}
			out.Values[i] = n
		default:
			return nil, conduiterr.New(connect.CodeUnsupportedType, "source field "+f.Name)
		}
	}
	for k := range md {
		if name, ok := strings.CutPrefix(k, MetaSourcePfx); ok {
			if _, idx := s.Field(name); idx < 0 {
				return nil, mismatch(k, fmt.Sprintf("not a field of the source block for debezium.compat %s", b.opts.Compat))
			}
		}
	}
	return out, nil
}

func txValue(md opencdc.Metadata) (*connect.Struct, error) {
	id, ok := md[MetaTxID]
	if !ok {
		for _, k := range []string{MetaTxTotal, MetaTxCollOrder} {
			if _, set := md[k]; set {
				return nil, mismatch(k, "set without "+MetaTxID)
			}
		}
		return nil, nil
	}
	total, err := strconv.ParseInt(md[MetaTxTotal], 10, 64)
	if err != nil {
		return nil, mismatch(MetaTxTotal, "missing or not an int64")
	}
	coll, err := strconv.ParseInt(md[MetaTxCollOrder], 10, 64)
	if err != nil {
		return nil, mismatch(MetaTxCollOrder, "missing or not an int64")
	}
	return &connect.Struct{Schema: transactionSchema, Values: []any{id, total, coll}}, nil
}

func mismatch(path, msg string) error {
	return conduiterr.New(connect.CodeRecordSchemaMismatch, path+": "+msg)
}
