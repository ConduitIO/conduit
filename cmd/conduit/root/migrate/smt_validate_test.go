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
	"github.com/conduitio/conduit/pkg/kcmigrate/migrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/smt/smttest"
	"github.com/matryer/is"
)

// TestRun_SMTProcessorsPassPipelinesValidate runs the SMT framework's
// fixture translators through the CLI's own Run, whose validator is the
// `pipelines validate` engine: the processors, IDs, conditions and settings
// the chain writes form a pipeline that validates. The fixtures are never
// registered outside tests.
func TestRun_SMTProcessorsPassPipelinesValidate(t *testing.T) {
	is := is.New(t)
	cfg := `{"name": "pg", "config": {
		"connector.class": "io.debezium.connector.postgresql.PostgresConnector",
		"transforms": "drop,split",
		"transforms.drop.type": "` + smttest.Drop + `",
		"transforms.drop.predicate": "p",
		"transforms.drop.negate": "true",
		"transforms.split.type": "` + smttest.Split + `$Value",
		"transforms.split.a": "x",
		"transforms.split.b": "y",
		"predicates": "p",
		"predicates.p.type": "` + smttest.HasMeta + `",
		"predicates.p.key": "k"}}`
	res, err := Run(context.Background(), migrate.Request{
		Inputs:      []kcmigrate.Input{{Name: "pg.json", Content: []byte(cfg)}},
		Translators: smttest.Registry(),
	})
	is.NoErr(err)
	var p string
	for _, f := range res.Files {
		if strings.HasPrefix(f.Path, "pipelines/") {
			p = string(f.Content)
		}
	}
	is.True(strings.Contains(p, "id: smt-split-2"))
	is.Equal(res.Report.Connectors[0].Transforms[1].Processor, "field.exclude")
}

// TestRun_PostgresTablesPassPipelinesValidate runs a Debezium Postgres config
// with a schema-qualified table.include.list through the CLI's own Run, whose
// validator is the `pipelines validate` engine: the translated tables are
// bare names (the source reads a dotted entry as one table name and refuses
// to start, #3350), the schema reaches the connection as search_path, and
// the pipeline validates.
func TestRun_PostgresTablesPassPipelinesValidate(t *testing.T) {
	is := is.New(t)
	cfg := `{"name": "pg", "config": {
		"connector.class": "io.debezium.connector.postgresql.PostgresConnector",
		"database.hostname": "db.internal.example",
		"database.user": "dbz",
		"database.dbname": "shop",
		"table.include.list": "inventory.orders,inventory.categories"}}`
	res, err := Run(context.Background(), migrate.Request{
		Inputs: []kcmigrate.Input{{Name: "pg.json", Content: []byte(cfg)}},
	})
	is.NoErr(err)
	var p string
	for _, f := range res.Files {
		if strings.HasPrefix(f.Path, "pipelines/") {
			p = string(f.Content)
		}
	}
	is.True(strings.Contains(p, "tables: orders,categories\n"))
	is.True(strings.Contains(p, " search_path=inventory\n"))
}
