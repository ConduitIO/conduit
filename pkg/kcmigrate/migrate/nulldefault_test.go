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
	)
	tests := []struct {
		name      string
		connector string
		worker    string
		want      bool
		paths     []string // the setting paths the reason names, when want
	}{
		{"unset", pgNullBase + jsonKey + jsonValue, "", true, []string{keyPath, valuePath}},
		{"both off on connector", pgNullBase + jsonKey + jsonValue + keyOff + valueOff, "", false, nil},
		{"set to true", pgNullBase + jsonKey + jsonValue + "key.converter.replace.null.with.default=true\n" + valueOff, "", true, []string{keyPath}},
		{"only key off", pgNullBase + jsonKey + jsonValue + keyOff, "", true, []string{valuePath}},
		{"only value off", pgNullBase + jsonKey + jsonValue + valueOff, "", true, []string{keyPath}},
		{"false is case-insensitive", pgNullBase + jsonKey + jsonValue + "key.converter.replace.null.with.default=FALSE\n" + valueOff, "", false, nil},
		{"worker only, both off", pgNullBase, jsonKey + jsonValue + keyOff + valueOff, false, nil},
		{"worker only, unset", pgNullBase, jsonKey + jsonValue, true, []string{keyPath, valuePath}},
		{
			// The connector names the converter, so Kafka Connect reads
			// its settings from the connector only: the worker's false
			// does not apply.
			"connector converter overrides worker setting",
			pgNullBase + jsonKey + jsonValue, keyOff + valueOff, true,
			[]string{keyPath, valuePath},
		},
		{
			"connector converter off, worker on",
			pgNullBase + jsonKey + jsonValue + keyOff + valueOff,
			"key.converter.replace.null.with.default=true\nvalue.converter.replace.null.with.default=true\n",
			false, nil,
		},
		{
			"converters without a schema are skipped",
			pgNullBase + "key.converter=org.apache.kafka.connect.storage.StringConverter\nvalue.converter=org.apache.kafka.connect.converters.ByteArrayConverter\n",
			"", false, nil,
		},
		{
			"other schema converter is noted conservatively",
			pgNullBase + jsonKey + "value.converter=io.confluent.connect.avro.AvroConverter\n" + keyOff,
			"", true,
			[]string{valuePath},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			n, ok := nullDefNote(t, tt.connector, tt.worker)
			is.Equal(ok, tt.want)
			if !ok {
				return
			}
			is.Equal(n.Code, "kc.default_differs")
			is.True(!n.Blocking)
			is.True(n.Fix != "")
			for _, p := range []string{keyPath, valuePath} {
				named := false
				for _, want := range tt.paths {
					named = named || want == p
				}
				// The reason names exactly the sides that replace.
				is.Equal(strings.Contains(n.Reason, p), named)
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
