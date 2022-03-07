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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/conduitio/conduit/pkg/plugin/sdk"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v4"
)

// Postgres requires use of a different variable placeholder.
var psql = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)

type Destination struct {
	sdk.UnimplementedDestination

	conn   *pgx.Conn
	config config
}

const (
	actionDelete = "delete"
	actionInsert = "insert"
	actionUpdate = "update"
)

type config struct {
	url           string
	tableName     string
	keyColumnName string
}

func NewDestination() sdk.Destination {
	return &Destination{}
}

func (d *Destination) Configure(ctx context.Context, cfg map[string]string) error {
	d.config = config{
		url:           cfg["url"],
		tableName:     cfg["table"],
		keyColumnName: cfg["keyColumnName"],
	}
	return nil
}

func (d *Destination) Open(ctx context.Context) error {
	if err := d.connect(ctx, d.config.url); err != nil {
		return fmt.Errorf("failed to connecto to postgres: %w", err)
	}
	return nil
}

func (d *Destination) Write(ctx context.Context, record sdk.Record) error {
	return d.write(ctx, record)
}

func (d *Destination) Flush(context.Context) error {
	return nil
}

func (d *Destination) Teardown(ctx context.Context) error {
	if d.conn != nil {
		return d.conn.Close(ctx)
	}
	return nil
}

func (d *Destination) connect(ctx context.Context, uri string) error {
	conn, err := pgx.Connect(ctx, uri)
	if err != nil {
		return fmt.Errorf("failed to open connection: %w", err)
	}
	d.conn = conn
	return nil
}

func (d *Destination) write(ctx context.Context, r sdk.Record) error {
	action, ok := r.Metadata["action"]
	if !ok {
		return d.upsert(ctx, r)
	}

	switch action {
	case actionDelete:
		return d.handleDelete(ctx, r)
	case actionInsert:
		return d.handleInsert(ctx, r)
	case actionUpdate:
		return d.handleUpdate(ctx, r)
	default:
		// NB: Do we want to default to upsert behavior? Or insert behavior?
		// Or something else?
		return d.upsert(ctx, r)
	}
}

func (d *Destination) handleInsert(ctx context.Context, r sdk.Record) error {
	return d.upsert(ctx, r)
}

func (d *Destination) handleUpdate(ctx context.Context, r sdk.Record) error {
	return d.upsert(ctx, r)
}

func (d *Destination) handleDelete(ctx context.Context, r sdk.Record) error {
	return fmt.Errorf("not impl")
}

func (d *Destination) upsert(ctx context.Context, r sdk.Record) error {
	payload, err := getPayload(r)
	if err != nil {
		return fmt.Errorf("failed to get payload: %w", err)
	}

	key, err := getKey(r)
	if err != nil {
		return fmt.Errorf("failed to get key: %w", err)
	}

	keyColumnName := getKeyColumnName(key, d.config.keyColumnName)

	tableName, err := d.getTableName(r.Metadata)
	if err != nil {
		return fmt.Errorf("failed to get table name for write: %w", err)
	}

	query, args, err := formatQuery(key, payload, keyColumnName, tableName)
	if err != nil {
		return fmt.Errorf("error formatting query: %w", err)
	}

	_, err = d.conn.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("insert exec failed: %w", err)
	}

	return nil
}

func getPayload(r sdk.Record) (sdk.StructuredData, error) {
	return structuredDataFormatter(r.Payload.Bytes())
}

func getKey(r sdk.Record) (sdk.StructuredData, error) {
	return structuredDataFormatter(r.Key.Bytes())
}

func structuredDataFormatter(raw []byte) (sdk.StructuredData, error) {
	if len(raw) == 0 {
		return sdk.StructuredData{}, nil
	}
	data := make(map[string]interface{})
	err := json.Unmarshal(raw, &data)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// formatQuery manually formats the UPSERT and ON CONFLICT query statements.
// the `ON CONFLICT` portion of this query needs to specify the constraint
// name.
// * In our case, we can only rely on the record.Key's parsed key value.
// * If other schema constraints prevent a write, this won't upsert on
// that conflict.
func formatQuery(
	key sdk.StructuredData,
	payload sdk.StructuredData,
	keyColumnName string,
	tableName string,
) (string, []interface{}, error) {
	upsertQuery := fmt.Sprintf("ON CONFLICT (%s) DO UPDATE SET", keyColumnName)
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

	colArgs, valArgs := formatColumnsAndValues(key, payload)

	query, args, err := psql.
		Insert(tableName).
		Columns(colArgs...).
		Values(valArgs...).
		SuffixExpr(sq.Expr(upsertQuery)).
		ToSql()
	if err != nil {
		return "", nil, fmt.Errorf("error formatting query: %w", err)
	}

	return query, args, nil
}

// formatColumnsAndValues turns the key and payload into a slice of ordered
// columns and values for upserting into Postgres.
func formatColumnsAndValues(key, payload sdk.StructuredData) ([]string, []interface{}) {
	var colArgs []string
	var valArgs []interface{}

	// range over both the key and payload values in order to format the
	// query for args and values in proper order
	for key, val := range key {
		colArgs = append(colArgs, key)
		valArgs = append(valArgs, val)
		delete(payload, key) // NB: Delete Key from payload arguments
	}

	for field, value := range payload {
		colArgs = append(colArgs, field)
		valArgs = append(valArgs, value)
	}

	return colArgs, valArgs
}

// return either the records metadata value for table or the default configured
// value for table. Otherwise it will error since we require some table to be
// set to write into.
func (d *Destination) getTableName(metadata map[string]string) (string, error) {
	tableName, ok := metadata["table"]
	if !ok {
		if d.config.tableName == "" {
			return "", fmt.Errorf("no table provided for default writes")
		}
		return d.config.tableName, nil
	}
	return tableName, nil
}

// getKeyColumnName will return the name of the first item in the key.
func getKeyColumnName(key sdk.StructuredData, defaultKeyName string) string {
	if len(key) > 1 {
		// Go maps aren't order preserving, so anything over len 1 will have
		// non deterministic results until we handle composite keys.
		panic("composite keys not yet supported")
	}
	for k := range key {
		return k
	}

	return defaultKeyName
}
