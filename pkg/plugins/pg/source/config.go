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

package source

import (
	"database/sql"
	"log"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins"

	sq "github.com/Masterminds/squirrel"
)

// Declare Postgres $ placeholder format
var psql = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)

// withTable sets the table that the Source should read from Postgres or errors
// if one isn't provided.
func (s *Source) withTable(cfg plugins.Config) error {
	table, ok := cfg.Settings["table"]
	if !ok {
		return ErrNoTable
	}
	s.table = table
	return nil
}

// withDB sets the DB object on the Source and Pings the DB or returns an error
func (s *Source) withDB(cfg plugins.Config) error {
	url, ok := cfg.Settings["url"]
	if !ok {
		return ErrInvalidURL
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		return cerrors.Errorf("failed to open source DB: %w", err)
	}
	err = db.Ping()
	if err != nil {
		return cerrors.Errorf("failed to ping db: %w", err)
	}
	// assign db if successfully pinged
	s.db = db
	return nil
}

// withKeyColumn sets the column used to key records.
// if one isn't set in the config, it will attempt to find one by calling
// getDefaultKeyColumn
func (s *Source) withKeyColumn(cfg plugins.Config) error {
	// determine primary key column
	key, ok := cfg.Settings["key"]
	if !ok {
		keyCol, err := getDefaultKeyColumn(s.db, s.table)
		if err != nil {
			return cerrors.Errorf("failed to auto configure key column: %w", err)
		}
		s.key = keyCol
	} else {
		log.Printf("manually setting key to [%s]", key)
		s.key = key
	}
	return nil
}

// withColumns takes a config and sets the columns property on the Source.
// If a "columns" value is set, it will set columns to that value and return.
// If no columns value is set, it attempts to query the database for the columns
// and defaults to returning all columns for the given table. If it fails to
// find any columns, it will return an error.
func (s *Source) withColumns(cfg plugins.Config) error {
	columns, ok := cfg.Settings["columns"]
	if !ok {
		log.Printf("no columns selected - defaulting to read all columns in [%s]", s.table)
		// assume that they want all columns recorded
		sql, args, err := psql.Select("*").From(s.table).Limit(1).ToSql()
		if err != nil {
			return cerrors.Errorf("failed to parse cols query: %w", err)
		}
		rows, err := s.db.Query(sql, args...)
		if err != nil || rows.Err() != nil {
			return cerrors.Errorf("failed to query during configuration: %w", err)
		}
		defer func() {
			err := rows.Close()
			if err != nil {
				log.Printf("failed to gracefully close rows: %s", err)
			}
		}()
		// since we selected all in our earlier query, this will contain a
		// list of all of the columns in the database
		columns, err := rows.Columns()
		if err != nil {
			return cerrors.Errorf("failed to get column names from rows: %w", err)
		}
		// set to all of the columns
		log.Printf("setting source to read columns [%+v]", columns)
		s.columns = columns
		return nil
	}

	// if they provide a columns list, sanitize it and set and return
	trimmed := strings.TrimSpace(columns)
	s.columns = strings.Split(trimmed, ",")

	return nil
}
