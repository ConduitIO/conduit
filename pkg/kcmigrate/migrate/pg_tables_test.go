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
	"regexp"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/matryer/is"
)

const pgBase = "name=pg\nconnector.class=io.debezium.connector.postgresql.PostgresConnector\n" +
	"database.hostname=db.internal.example\ndatabase.user=dbz\ndatabase.dbname=shop\n" +
	"key.converter=org.apache.kafka.connect.json.JsonConverter\nvalue.converter=org.apache.kafka.connect.json.JsonConverter\n"

func pgRun(t *testing.T, extra string) (kcmigrate.ReportConnector, string) {
	t.Helper()
	return pgRunConfig(t, pgBase+extra)
}

func pgRunConfig(t *testing.T, config string) (kcmigrate.ReportConnector, string) {
	t.Helper()
	e, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), e, withValidator(Request{
		ConduitVersion: "v", RequiredSettings: BuiltinRequired,
		Inputs: []kcmigrate.Input{in("pg.properties", config)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	p := file(res, "pipelines/pg.yaml")
	checkPipeline(t, p)
	return res.Report.Connectors[0], p
}

var (
	tablesLine = regexp.MustCompile(`(?m)^\s+tables: (.*)$`)
	urlLine    = regexp.MustCompile(`(?m)^\s+url: (.*)$`)
)

func setting(t *testing.T, re *regexp.Regexp, p string) string {
	t.Helper()
	m := re.FindStringSubmatch(p)
	if m == nil {
		t.Fatalf("setting not in pipeline:\n%s", p)
	}
	return m[1]
}

// TestPGTables covers how table.include.list becomes the builtin:postgres
// source's tables (#3350). The source quotes each entry as one identifier
// found through search_path, so an entry is never schema-qualified: one
// schema translates to bare names plus search_path=<schema> in url, and
// anything that cannot be expressed exactly blocks with tables left unset.
func TestPGTables(t *testing.T) {
	tests := []struct {
		name       string
		list       string
		key        string // default table.include.list
		tables     string // want, when translated
		searchPath string // want, when translated
		code       string // want, when blocking
		reason     string // a substring of the blocking reason
	}{
		{name: "public schema", list: "public.orders", tables: "orders", searchPath: "public"},
		{name: "another schema", list: "inventory.orders,inventory.categories", tables: "orders,categories", searchPath: "inventory"},
		{name: "spaces and a duplicate", list: " inventory.orders , inventory.categories,inventory.orders ", tables: "orders,categories", searchPath: "inventory"},
		// The legacy name is approximated (kc.key_deprecated) and translates the same.
		{name: "legacy alias", key: "table.whitelist", list: "public.orders", tables: "orders", searchPath: "public"},
		{name: "two schemas", list: "inventory.orders,sales.orders", code: "kc.target_capability_missing", reason: "more than one schema (inventory, sales)"},
		{name: "two schemas, different tables", list: "public.orders,sales.customers", code: "kc.target_capability_missing", reason: "conduit-connector-postgres#277"},
		{name: "bare name", list: "orders", code: "kc.key_unsupported", reason: "has no schema"},
		{name: "bare name among qualified", list: "public.orders,customers", code: "kc.key_unsupported", reason: "customers has no schema"},
		{name: "mixed-case table", list: "public.Orders", code: "kc.key_unsupported", reason: "uppercase"},
		{name: "mixed-case schema", list: "Sales.orders", code: "kc.key_unsupported", reason: "uppercase"},
		{name: "system schema", list: "pg_catalog.pg_class", code: "kc.key_unsupported", reason: "system schema pg_catalog"},
		{name: "information_schema", list: "information_schema.tables", code: "kc.key_unsupported", reason: "system schema"},
		{name: "pg_ table", list: "public.pg_orders", code: "kc.key_unsupported", reason: "pg_catalog before the search_path"},
		{name: "regex wildcard", list: "public.*", code: "kc.key_unsupported", reason: "regex metacharacters"},
		{name: "regex star in a name", list: "inventory.ord*", code: "kc.key_unsupported", reason: "regex metacharacters"},
		{name: "escaped dot", list: `inventory\\.orders`, code: "kc.key_unsupported", reason: "regex metacharacters"},
		{name: "two dots", list: "shop.inventory.orders", code: "kc.key_unsupported", reason: "more than one dot"},
		{name: "empty element", list: "public.orders,,public.customers", code: "kc.key_unsupported", reason: "empty element"},
		{name: "trailing comma", list: "public.orders,", code: "kc.key_unsupported", reason: "empty element"},
		{name: "empty value", list: "", code: "kc.key_unsupported", reason: "is empty"},
		{name: "digit-leading table", list: "inventory.2024_orders", tables: "2024_orders", searchPath: "inventory"},
		{name: "digit-leading schema", list: "2024.orders", code: "kc.key_unsupported", reason: "not a plain schema.table"},
		{name: "space inside a name", list: "public.my orders", code: "kc.key_unsupported", reason: "not a plain schema.table"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			key := tt.key
			if key == "" {
				key = "table.include.list"
			}
			rc, p := pgRun(t, key+"="+tt.list+"\n")
			e := entryFor(rc, key)
			tables, url := setting(t, tablesLine, p), setting(t, urlLine, p)
			if tt.code == "" {
				if key == "table.whitelist" {
					is.Equal(e.Code, "kc.key_deprecated")
				} else {
					is.Equal(e.Status, kcmigrate.StatusTranslated)
				}
				is.True(!e.Blocking)
				is.Equal(e.Target.ConfigPath, "/pipelines/0/connectors/0/settings/tables")
				is.Equal(tables, tt.tables)
				is.True(strings.HasSuffix(url, " search_path="+tt.searchPath)) // url
				is.True(strings.Contains(e.Reason, "search_path="+tt.searchPath))
				for _, n := range rc.Notes { // a list is set: no absent-list note
					is.True(n.Rule != "debezium-postgres/defaults/table.include.list")
				}
				return
			}
			is.Equal(e.Status, kcmigrate.StatusUnsupported)
			is.True(e.Blocking)
			is.Equal(e.Code, tt.code)
			is.True(e.Fix != "")
			if !strings.Contains(e.Reason, tt.reason) {
				t.Fatalf("reason %q does not contain %q", e.Reason, tt.reason)
			}
			// Nothing the source refuses or reads in place of the list: no
			// dotted entry, no "*" widening, no search_path.
			is.Equal(tables, "${KC_PG_SOURCE_TABLES}")
			is.True(!strings.Contains(url, "search_path"))
			is.Equal(rc.PipelineStatus, kcmigrate.PipelineStopped)
		})
	}
}

// Without table.include.list Debezium reads every table of every
// non-system schema, and the source's "*" reads only public: there is no
// exact translation, so the absent list is a blocking note and tables stays a
// variable (no "*", no search_path).
func TestPGTables_AbsentListBlocks(t *testing.T) {
	is := is.New(t)
	rc, p := pgRun(t, "")
	is.Equal(setting(t, tablesLine, p), "${KC_PG_SOURCE_TABLES}")
	is.True(!strings.Contains(setting(t, urlLine, p), "search_path"))
	var absent, required bool
	for _, n := range rc.Notes {
		absent = absent || (n.Rule == "debezium-postgres/defaults/table.include.list" && n.Blocking && strings.Contains(n.Reason, "only the public schema"))
		required = required || (n.Rule == "required/source/tables" && n.Blocking)
	}
	is.True(absent)
	is.True(required)
	is.Equal(rc.PipelineStatus, kcmigrate.PipelineStopped)
}

// table.include.list and its legacy alias with different schemas conflict;
// the losing key's schema never reaches search_path (else --accept-gaps
// would read sales.orders in place of inventory.orders).
func TestPGTables_AliasConflictKeepsTheWinnersSchema(t *testing.T) {
	is := is.New(t)
	rc, p := pgRun(t, "table.include.list=inventory.orders\ntable.whitelist=sales.orders\n")
	is.Equal(entryFor(rc, "table.whitelist").Code, "kc.key_conflict")
	is.Equal(setting(t, tablesLine, p), "orders")
	url := setting(t, urlLine, p)
	is.True(strings.HasSuffix(url, " search_path=inventory"))
	is.True(!strings.Contains(url, "sales"))
}

// Without database.* keys the url is a variable; the search_path the bare
// tables depend on is carried by a blocking note, so filling the variable
// cannot silently drop it.
func TestPGTables_NoConnectionKeysCarrySearchPath(t *testing.T) {
	is := is.New(t)
	rc, p := pgRunConfig(t, "name=pg\nconnector.class=io.debezium.connector.postgresql.PostgresConnector\n"+
		"key.converter=org.apache.kafka.connect.json.JsonConverter\nvalue.converter=org.apache.kafka.connect.json.JsonConverter\n"+
		"table.include.list=inventory.orders\n")
	is.Equal(setting(t, tablesLine, p), "orders")
	is.Equal(setting(t, urlLine, p), "${KC_PG_SOURCE_URL}")
	var carried bool
	for _, n := range rc.Notes {
		carried = carried || (n.Rule == "pg-url/search_path" && n.Blocking && n.Code == "kc.setting_required" && strings.Contains(n.Fix, "search_path=inventory"))
	}
	is.True(carried)
	is.Equal(rc.PipelineStatus, kcmigrate.PipelineStopped)
}

// A refused table or schema selection key never falls back to tables "*"
// (the public schema), which --accept-gaps would run: tables stays a
// variable with a blocking kc.setting_required note, and no search_path is
// written.
func TestPGTables_RefusedSelectionDoesNotWidenToAll(t *testing.T) {
	for _, kv := range []string{
		"schema.include.list=inventory",
		"schema.whitelist=inventory",
		"schema.exclude.list=audit",
		"schema.blacklist=audit",
		"table.exclude.list=public.audit",
		"table.blacklist=public.audit",
		"table.include.list=public.*",
	} {
		t.Run(kv, func(t *testing.T) {
			is := is.New(t)
			rc, p := pgRun(t, kv+"\n")
			is.True(entryFor(rc, strings.SplitN(kv, "=", 2)[0]).Blocking)
			is.Equal(setting(t, tablesLine, p), "${KC_PG_SOURCE_TABLES}")
			is.True(!strings.Contains(setting(t, urlLine, p), "search_path"))
			var required bool
			for _, n := range rc.Notes {
				required = required || (n.Code == "kc.setting_required" && n.Rule == "required/source/tables" && n.Blocking)
			}
			is.True(required)
			is.Equal(rc.PipelineStatus, kcmigrate.PipelineStopped)
		})
	}
}
