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

package kcmigrate

import (
	"os"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

type cfgWant struct{ key, val, ptr string }

func configOf(c Connector) []cfgWant {
	out := make([]cfgWant, 0, len(c.Config))
	for _, kv := range c.Config {
		out = append(out, cfgWant{kv.Key, kv.Value.Reveal(), kv.Origin.Pointer})
	}
	return out
}

func mustParse(t *testing.T, name, content string, f Format) []Connector {
	t.Helper()
	cs, err := ParseConnectors(Input{Name: name, Content: []byte(content)}, f)
	if err != nil {
		t.Fatalf("ParseConnectors: %v", err)
	}
	return cs
}

func TestParseConnectors_JSONConnectorForm(t *testing.T) {
	is := is.New(t)
	cs := mustParse(t, "c.json", `{
	  "name": "orders-cdc",
	  "config": {
	    "connector.class": "io.debezium.connector.postgresql.PostgresConnector",
	    "tasks.max": 1,
	    "tombstones.on.delete": false,
	    "a/b~c": "x",
	    "name": "orders-cdc",
	    "tasks.max": "2"
	  },
	  "tasks": [{"connector": "orders-cdc", "task": 0}],
	  "type": "source"
	}`, "")
	is.Equal(len(cs), 1)
	c := cs[0]
	is.Equal(c.Name, "orders-cdc")
	is.Equal(c.Class, "io.debezium.connector.postgresql.PostgresConnector")
	is.Equal(c.Type, "source")
	is.Equal(c.File, "c.json")
	is.Equal(configOf(c), []cfgWant{
		{"connector.class", "io.debezium.connector.postgresql.PostgresConnector", "/config/connector.class"},
		{"tasks.max", "1", "/config/tasks.max"},
		{"tombstones.on.delete", "false", "/config/tombstones.on.delete"},
		{"a/b~c", "x", "/config/a~1b~0c"},
		{"name", "orders-cdc", "/config/name"},
		{"tasks.max", "2", "/config/tasks.max"}, // duplicates are kept, in order
	})
}

func TestParseConnectors_TopLevelNameWins(t *testing.T) {
	is := is.New(t)
	cs := mustParse(t, "c.json", `{"name": "top", "config": {"name": "inner", "connector.class": "X"}, "initial_state": "STOPPED"}`, FormatJSON)
	is.Equal(cs[0].Name, "top")
	is.Equal(cs[0].State, "STOPPED")
	is.Equal(len(cs[0].Config), 2) // the inner name is still an input key
}

func TestParseConnectors_BareMap(t *testing.T) {
	is := is.New(t)
	cs := mustParse(t, "c.json", "\ufeff"+`{"connector.class": "X", "name": "n", "x": 1.50}`, "")
	is.Equal(cs[0].Name, "n")
	is.Equal(configOf(cs[0]), []cfgWant{{"connector.class", "X", "/connector.class"}, {"name", "n", "/name"}, {"x", "1.50", "/x"}})
}

func TestParseConnectors_RESTExport(t *testing.T) {
	is := is.New(t)
	content, err := os.ReadFile("testdata/parse/rest-export.json")
	is.NoErr(err)
	for _, f := range []Format{"", FormatJSON, FormatRESTExport} {
		cs, err := ParseConnectors(Input{Name: "export.json", Content: content}, f)
		is.NoErr(err)
		is.Equal(len(cs), 2)
		is.Equal(cs[0].Name, "orders-cdc")
		is.Equal(cs[0].State, "RUNNING")
		is.Equal(cs[0].Type, "source")
		is.Equal(cs[0].Config[0].Origin.Pointer, "/orders-cdc/info/config/connector.class")
		is.Equal(cs[1].Name, "s3-sink")
		is.Equal(cs[1].State, "PAUSED")
		is.Equal(cs[1].Class, "io.confluent.connect.s3.S3SinkConnector")
	}
}

func TestParseConnectors_Properties(t *testing.T) {
	is := is.New(t)
	cs := mustParse(t, "c.properties", "name=jdbc\nconnector.class=io.confluent.connect.jdbc.JdbcSinkConnector\n", "")
	is.Equal(cs[0].Name, "jdbc")
	is.Equal(cs[0].Class, "io.confluent.connect.jdbc.JdbcSinkConnector")
	is.Equal(cs[0].Config[1].Origin, Origin{File: "c.properties", Line: 2, Column: 1})
}

func TestParseConnectors_Errors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		format  Format
		code    conduiterr.Code
		ptr     string // expected ConfigPath suffix
	}{
		{"syntax", `{"config": {"a": "secret-value-1"`, "", CodeInputUnreadable, "c.json"},
		{"trailing data", `{"a": "b"} {"c": "d"}`, "", CodeInputUnreadable, "c.json"},
		{"invalid utf-8", "{\"a\": \"\xff\"}", "", CodeInputUnreadable, "c.json"},
		{"not an object", `["a"]`, "", CodeInputUnrecognized, "c.json"},
		{"empty object", `{}`, "", CodeInputUnrecognized, "c.json"},
		{"null config value", `{"config": {"a": null}}`, "", CodeInputUnrecognized, "#/config/a"},
		{"object config value", `{"config": {"a": {"b": "c"}}}`, "", CodeInputUnrecognized, "#/config/a"},
		{"array in bare map", `{"a": "b", "c": ["d"]}`, "", CodeInputUnrecognized, "c.json"},
		{"unknown top-level member", `{"name": "n", "config": {}, "extra": "x"}`, "", CodeInputUnrecognized, "#/extra"},
		{"duplicate config member", `{"config": {}, "config": {}}`, "", CodeInputUnrecognized, "#/config"},
		{"name not a string", `{"name": 1, "config": {}}`, "", CodeInputUnrecognized, "#/name"},
		{"export without info", `{"x": {"status": {"connector": {"state": "RUNNING"}}}}`, "", CodeInputUnrecognized, "#/x"},
		{"export unknown member", `{"x": {"info": {"config": {}}, "extra": {}}}`, "", CodeInputUnrecognized, "#/x/extra"},
		{"connector form as rest-export", `{"name": "n", "config": {}}`, FormatRESTExport, CodeInputUnrecognized, "c.json"},
		{"too deep", strings.Repeat(`{"a":`, MaxJSONDepth+2) + `"b"` + strings.Repeat(`}`, MaxJSONDepth+2), "", CodeInputTooLarge, ""},
		{"unknown format", `a=b`, "xml", CodeInputUnrecognized, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			_, err := ParseConnectors(Input{Name: "c.json", Content: []byte(tt.content)}, tt.format)
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, tt.code)
			is.True(strings.HasSuffix(ce.ConfigPath, tt.ptr))
			is.True(ce.Suggestion != "")
			// Errors never quote input values.
			is.True(!strings.Contains(ce.Error(), "secret-value-1"))
		})
	}
}

func TestParseConnectors_MirrorMakerDedicated(t *testing.T) {
	is := is.New(t)
	_, err := ParseConnectors(Input{Name: "mm2.properties", Content: []byte("clusters=A,B\nA.bootstrap.servers=a:9092\nA->B.enabled=true\n")}, "")
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeInputUnrecognized)
	is.True(strings.Contains(ce.Suggestion, "connector form"))

	// The connector form of MirrorSourceConnector is read normally.
	cs := mustParse(t, "mm.properties", "connector.class=org.apache.kafka.connect.mirror.MirrorSourceConnector\nclusters=A,B\nA->B.enabled=true\n", "")
	is.Equal(len(cs[0].Config), 3)
}
