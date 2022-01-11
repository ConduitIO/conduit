// Copyright © 2022 Meroxa, Inc.
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

package destination

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/record"

	sq "github.com/Masterminds/squirrel"
	_ "github.com/lib/pq" // Blank import to register the PostgreSQL driver
)

// Destination fulfills the Destination interface
type Destination struct {
	Position record.Position

	db *sql.DB
}

// Open attempts to open a database connection to Postgres.
func (s *Destination) Open(ctx context.Context, cfg plugins.Config) error {
	url, ok := cfg.Settings["url"]
	if !ok {
		return cerrors.New("must provide a connection URL")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		return cerrors.Errorf("failed to open source DB: %w", err)
	}
	err = db.Ping()
	if err != nil {
		return cerrors.Errorf("failed to ping opened db: %w", err)
	}
	s.db = db
	return nil
}

// Teardown will attempt to gracefully close the database connection.
func (s *Destination) Teardown() error {
	return s.db.Close()
}

// Validate will validate a configuration.
func (s *Destination) Validate(cfg plugins.Config) error {
	return nil // TODO
}

// Write will write the record to the Destination and then returns the written
// Position or an error.
func (s *Destination) Write(ctx context.Context, r record.Record) (record.Position, error) {
	// Postgres requires use of a different variable placeholder.
	psql := sq.StatementBuilder.PlaceholderFormat(sq.Dollar)

	// assert that we're working with structured data
	payload, ok := r.Payload.(record.StructuredData)
	if !ok {
		// TODO: Handle Raw Data with a Warehouse Writer
		return s.Position, cerrors.New("cannot handle unstructured data")
	}

	// turn the structured data into a slice of ordered columns and values
	var colArgs []string
	var valArgs []interface{}
	for field, value := range payload {
		colArgs = append(colArgs, field)
		valArgs = append(valArgs, value)
	}

	// keyCol holds the name of the column that records are keyed against.
	var keyCol string
	if r.Key != nil {
		d, ok := r.Key.(record.StructuredData)
		if !ok {
			return s.Position, cerrors.Errorf("failed to parse key: %+v", r.Key)
		}

		// TODO: Handle composite keys
		// Explicitly error out if we detect more than one key.
		if len(d) > 1 {
			return s.Position, cerrors.New("composite keys not yet supported")
		}

		// add key data to the query
		for k, v := range d {
			colArgs = append(colArgs, k)
			valArgs = append(valArgs, v)
			keyCol = k // set the key column variable for later reference
		}
	}

	// check if the record has a table set. if it does, write the record to that
	// table. if it doesn't, error out.
	table, ok := r.Metadata["table"]
	if !ok {
		return s.Position, cerrors.New("record must provide a table name")
	}

	// manually format the upsert and on conflict part of the query.
	// the ON CONFLICT portion of this query needs to specify the constraint
	// name. in our case, we can only rely on the record.Key's parsed
	// StructuredData key.
	// note: if other schema constraints prevent a write, this won't upsert on
	// that conflict.
	upsertQuery := fmt.Sprintf("ON CONFLICT (%s) DO UPDATE SET", keyCol)

	// range over the record's payload and create the upsert query and args
	for column := range payload {
		// tuples form a comma separated list, so they need a comma at the end.
		// `EXCLUDED` references the new record's values. This will overwrite
		// every column's value except for the key column.
		tuple := fmt.Sprintf("%s=EXCLUDED.%s,", column, column)
		// TODO: Consider removing this space.
		upsertQuery += " "
		// add the tuple to the query string
		upsertQuery += tuple
	}

	// remove the last comma from the list of tuples
	upsertQuery = strings.TrimSuffix(upsertQuery, ",")

	// we have to manually append a semi colon to the upsert sql;
	upsertQuery += ";"

	// prepare SQL to insert cols and args into the appropriate table.
	// suffix sql and args for upsert behavior.
	query, args, err := psql.Insert(table).
		Columns(colArgs...).
		Values(valArgs...).
		SuffixExpr(sq.Expr(upsertQuery)).
		ToSql()
	if err != nil {
		return s.Position, cerrors.Errorf("error formatting query: %w", err)
	}

	// attempt to run the query
	_, err = s.db.Exec(query, args...)
	if err != nil {
		// return current position that hasn't been updated and the error
		return s.Position, cerrors.Errorf("insert exec failed: %w", err)
	}

	// assign s.Position after we've successfully written the record
	s.Position = r.Position
	return s.Position, nil
}
