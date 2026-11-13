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

package migrate

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/matryer/is"
)

const nullDefRule = "debezium-postgres/notes/replace-null-with-default"

const (
	jsonClass  = "org.apache.kafka.connect.json.JsonConverter"
	jsonKey    = "key.converter=" + jsonClass + "\n"
	jsonValue  = "value.converter=" + jsonClass + "\n"
	keyOff     = "key.converter.replace.null.with.default=false\n"
	valueOff   = "value.converter.replace.null.with.default=false\n"
	pgNullBase = "name=pg\nconnector.class=io.debezium.connector.postgresql.PostgresConnector\n" +
		"database.hostname=db.internal.example\ndatabase.user=dbz\ndatabase.dbname=shop\n"
)

func nullDefNote(t *testing.T, connector, worker string) (kcmigrate.Note, bool) {
	t.Helper()
	e, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	req := Request{
		ConduitVersion: "v", RequiredSettings: BuiltinRequired,
		Inputs: []kcmigrate.Input{in("pg.properties", connector)},
	}
	if worker != "" {
		w := in("w.properties", worker)
		req.Worker = &w
	}
	res, err := Run(context.Background(), e, withValidator(req))
	if err != nil {
		t.Fatal(err)
	}
	rc := res.Report.Connectors[len(res.Report.Connectors)-1]
	for _, n := range rc.Notes {
		if n.Rule == nullDefRule {
			return n, true
		}
	}
	return kcmigrate.Note{}, false
}

func TestNullDefaultNote(t *testing.T) {
	const (
		keyPath   = "key.converter.replace.null.with.default"
		valuePath = "value.converter.replace.null.with.default"
		keyIgn    = "key.converter.ignore.default.for.nullables"
		valueIgn  = "value.converter.ignore.default.for.nullables"
		strKey    = "key.converter=org.apache.kafka.connect.storage.StringConverter\n"
		strValue  = "value.converter=org.apache.kafka.connect.storage.StringConverter\n"
		avroKey   = "key.converter=io.confluent.connect.avro.AvroConverter\n"
		avroValue = "value.converter=io.confluent.connect.avro.AvroConverter\n"
		unkValue  = "value.converter=com.example.CustomConverter\n"
	)
	tests := []struct {
		name      string
		connector string
		worker    string
		want      []string // each named in the reason, when a note is expected
		not       []string // none named in the reason
	}{
		{"unset", pgNullBase + jsonKey + jsonValue, "", []string{keyPath, valuePath}, nil},
		{"both off on connector", pgNullBase + jsonKey + jsonValue + keyOff + valueOff, "", nil, nil},
		{"set to true", pgNullBase + jsonKey + jsonValue + "key.converter.replace.null.with.default=true\n" + valueOff, "", []string{keyPath}, []string{valuePath}},
		{"only key off", pgNullBase + jsonKey + jsonValue + keyOff, "", []string{valuePath}, []string{keyPath}},
		{"only value off", pgNullBase + jsonKey + jsonValue + valueOff, "", []string{keyPath}, []string{valuePath}},
		{"false is case-insensitive", pgNullBase + jsonKey + jsonValue + "key.converter.replace.null.with.default=FALSE\n" + valueOff, "", nil, nil},
		{"worker only, both off", pgNullBase, jsonKey + jsonValue + keyOff + valueOff, nil, nil},
		{"worker only, unset", pgNullBase, jsonKey + jsonValue, []string{keyPath, valuePath}, nil},
		{
			// The connector names the converter, so Kafka Connect reads
			// its settings from the connector only: the worker's false
			// does not apply.
			"connector converter overrides worker setting",
			pgNullBase + jsonKey + jsonValue, keyOff + valueOff,
			[]string{keyPath, valuePath},
			nil,
		},
		{
			"connector off, worker on",
			pgNullBase + jsonKey + jsonValue + keyOff + valueOff,
			"key.converter.replace.null.with.default=true\nvalue.converter.replace.null.with.default=true\n",
			nil, nil,
		},
		{
			// Precedence: the connector's class wins over the worker's, so the
			// worker's JsonConverter does not apply to a StringConverter side.
			"worker JsonConverter, connector StringConverter",
			pgNullBase + strKey + strValue, jsonKey + jsonValue, nil, nil,
		},
		{
			"worker StringConverter, connector JsonConverter",
			pgNullBase + jsonKey + jsonValue, strKey + strValue,
			[]string{keyPath, valuePath},
			nil,
		},
		{
			// Both layers name JsonConverter: only the connector's setting counts.
			"both name JsonConverter, connector off, worker unset",
			pgNullBase + jsonKey + jsonValue + keyOff + valueOff, jsonKey + jsonValue, nil, nil,
		},
		{
			"both name JsonConverter, connector unset, worker off",
			pgNullBase + jsonKey + jsonValue, jsonKey + jsonValue + keyOff + valueOff,
			[]string{keyPath, valuePath},
			nil,
		},
		{"converters without a schema are skipped", pgNullBase + strKey + "value.converter=org.apache.kafka.connect.converters.ByteArrayConverter\n", "", nil, nil},
		{
			// Confluent converters use ignore.default.for.nullables, not
			// replace.null.with.default.
			"Confluent converter unset",
			pgNullBase + jsonKey + avroValue + keyOff, "",
			[]string{valueIgn},
			[]string{valuePath, keyPath, keyIgn},
		},
		{
			"Confluent converter ignores replace.null.with.default",
			pgNullBase + avroKey + avroValue + keyOff + valueOff, "",
			[]string{keyIgn, valueIgn},
			[]string{keyPath, valuePath},
		},
		{"Confluent converter set to true", pgNullBase + avroKey + avroValue + "key.converter.ignore.default.for.nullables=true\nvalue.converter.ignore.default.for.nullables=TRUE\n", "", nil, nil},
		{
			"Confluent converter set to false",
			pgNullBase + avroKey + avroValue + "key.converter.ignore.default.for.nullables=true\nvalue.converter.ignore.default.for.nullables=false\n",
			"",
			[]string{valueIgn},
			[]string{keyIgn},
		},
		{
			"Protobuf and JSON Schema use the same setting",
			pgNullBase + "key.converter=io.confluent.connect.protobuf.ProtobufConverter\nvalue.converter=io.confluent.connect.json.JsonSchemaConverter\n",
			"",
			[]string{keyIgn, valueIgn},
			[]string{keyPath, valuePath},
		},
		{
			"unknown converter names no setting",
			pgNullBase + jsonKey + unkValue + keyOff, "",
			[]string{"value.converter is not a converter this version"},
			[]string{valuePath, valueIgn},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			n, ok := nullDefNote(t, tt.connector, tt.worker)
			is.Equal(ok, len(tt.want) > 0)
			if !ok {
				return
			}
			is.Equal(n.Code, "kc.default_differs")
			is.True(!n.Blocking)
			is.True(n.Fix != "")
			is.True(!strings.Contains(n.Reason, "wrote a column's default in place"))
			for _, w := range tt.want {
				is.True(strings.Contains(n.Reason, w))
			}
			for _, w := range tt.not {
				is.True(!strings.Contains(n.Reason, w))
				is.True(!strings.Contains(n.Fix, w))
			}
		})
	}
}

// Only Debezium sources carry the note: a MirrorMaker source moves bytes.
func TestNullDefaultNote_OnlyDebeziumSources(t *testing.T) {
	is := is.New(t)
	e, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	mm := "name=mm\nconnector.class=org.apache.kafka.connect.mirror.MirrorSourceConnector\n" + jsonKey + jsonValue +
		"source.cluster.alias=a\ntarget.cluster.alias=b\nsource.cluster.bootstrap.servers=x:9092\ntarget.cluster.bootstrap.servers=y:9092\n"
	res, err := Run(context.Background(), e, withValidator(Request{
		ConduitVersion: "v", RequiredSettings: BuiltinRequired,
		Inputs: []kcmigrate.Input{in("mm.properties", mm)},
	}))
	is.NoErr(err)
	for _, n := range res.Report.Connectors[0].Notes {
		is.True(!strings.HasSuffix(n.Rule, "replace-null-with-default"))
	}
}
