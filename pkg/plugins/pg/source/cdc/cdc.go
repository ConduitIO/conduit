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

package cdc

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/plugin/sdk"

	"github.com/batchcorp/pgoutput"
	"github.com/jackc/pgx"
)

// Config holds configuration values for our V1 Source. It is parsed from the
// map[string]string passed to the Connector at Configure time.
type Config struct {
	Position        sdk.Position
	URL             string
	SlotName        string
	PublicationName string
	TableName       string
	KeyColumnName   string
	Columns         []string
}

// Iterator listens for events from the WAL and pushes them into its buffer.
// It iterates through that Buffer so that we have a controlled way to get 1
// record from our CDC buffer without having to expose a loop to the main Read.
type Iterator struct {
	wg *sync.WaitGroup

	config Config

	lsn        uint64 // TODO: or maybe use an LSN type here
	messages   chan sdk.Record
	killswitch context.CancelFunc

	sub *pgoutput.Subscription
	db  *pgx.Conn
}

// NewCDCIterator takes a config and returns up a new CDCIterator or returns an
// error.
func NewCDCIterator(ctx context.Context, config Config) (*Iterator, error) {
	wctx, cancel := context.WithCancel(ctx)
	i := &Iterator{
		wg:         &sync.WaitGroup{},
		config:     config,
		messages:   make(chan sdk.Record),
		killswitch: cancel,
	}

	err := i.connectDB()
	if err != nil {
		return nil, cerrors.Errorf("failed to connect to postgres: %w", err)
	}

	err = i.attachSubscription()
	if err != nil {
		return nil, cerrors.Errorf("failed to setup subscription %w", err)
	}

	err = i.setPosition(config.Position)
	if err != nil {
		return nil, cerrors.Errorf("failed to set starting position: %w", err)
	}

	go i.listen(wctx)

	return i, nil
}

func (i *Iterator) setPosition(pos sdk.Position) error {
	if pos == nil || string(pos) == "" {
		i.lsn = 0
		return nil
	}

	lsn, err := parsePosition(string(pos))
	if err != nil {
		return err
	}
	i.lsn = lsn
	return nil
}

// listen is meant to be used in a goroutine. It starts the subscription
// passed to it and handles the the subscription flush
func (i *Iterator) listen(ctx context.Context) {
	i.wg.Add(1)
	defer cleanupListener(i.wg, i.sub)

	sdk.Logger(ctx).Printf("starting subscription at position %d", i.lsn)
	err := i.sub.Start(ctx, i.lsn, i.registerMessageHandlers())
	if err != nil {
		if err == context.Canceled {
			return
		}
		return
	}
}

func cleanupListener(wg *sync.WaitGroup, sub *pgoutput.Subscription) {
	sub.Flush()
	wg.Done()
}

// Next returns the next record in the buffer. This is a blocking operation
// so it should only be called if we've checked that HasNext is true or else
// it will block until a record is inserted into the queue.
func (i *Iterator) Next(ctx context.Context) (sdk.Record, error) {
	for {
		select {
		case r := <-i.messages:
			return r, nil
		case <-ctx.Done():
			return sdk.Record{}, sdk.ErrBackoffRetry
		}
	}
}

func (i *Iterator) Ack(ctx context.Context, pos sdk.Position) error {
	n, err := parsePosition(string(pos))
	if err != nil {
		return cerrors.Errorf("failed to parse position")
	}
	return i.sub.AdvanceLSN(n)
}

// push pushes a record into the buffer.
func (i *Iterator) push(r sdk.Record) {
	i.messages <- r
}

// Teardown kills the CDC subscription and waits for it to be done, closes its
// connection to the database, then cleans up its slot and publication.
func (i *Iterator) Teardown() error {
	i.killswitch()
	i.wg.Wait()
	defer i.db.Close()

	termErr := i.terminateBackend()
	dropReplErr := i.dropReplicationSlot()
	dropPubErr := i.dropPublication()
	return multierror.Append(termErr, dropPubErr, dropReplErr)
}

// attachSubscription builds a subscription with its own dedicated replication
// connection. It prepares a replication slot and publication for the connector
// if they're not yet setup with sane defaults if they're not configured.
func (i *Iterator) attachSubscription() error {
	if i.config.PublicationName == "" {
		i.config.PublicationName = "conduitpub"
	}
	if i.config.SlotName == "" {
		i.config.SlotName = "conduitslot"
	}

	err := i.configureColumns()
	if err != nil {
		return cerrors.Errorf("failed to find table columns: %w", err)
	}

	err = i.configureKeyColumn()
	if err != nil {
		return cerrors.Errorf("failed to find key: %w", err)
	}

	replConn, err := getReplicationConnection(i.config.URL)
	if err != nil {
		return cerrors.Errorf("failed to get replication conn: %w", err)
	}

	err = i.createPublicationForTable()
	if err != nil {
		return cerrors.Errorf("failed to create publication: %w", err)
	}

	err = replConn.CreateReplicationSlot(i.config.SlotName, "pgoutput")
	if err != nil {
		if !strings.Contains(err.Error(), "SQLSTATE 42710") {
			return cerrors.Errorf("failed to create replication slot: %v", err)
		}
	}

	var maxWalRetain uint64
	var failOnHandler bool
	sub := pgoutput.NewSubscription(
		replConn,
		i.config.SlotName,
		i.config.PublicationName,
		maxWalRetain,
		failOnHandler)

	i.sub = sub
	return nil
}

func (i *Iterator) createPublicationForTable() error {
	_, err := i.db.Exec(
		fmt.Sprintf("create publication %s for table %s;",
			i.config.PublicationName,
			i.config.TableName))
	if err != nil {
		if !strings.Contains(err.Error(), "SQLSTATE 42710") {
			return cerrors.Errorf("failed to create publication %s: %w",
				i.config.SlotName, err)
		}
	}
	return nil
}

func (i *Iterator) connectDB() error {
	rc, err := getReplicationConnection(i.config.URL)
	if err != nil {
		return cerrors.Errorf("failed to get replication connection: %w", err)
	}
	i.db = rc.Conn
	return nil
}

// registerMessageHandlers returns a Handler that switches on message type.
func (i *Iterator) registerMessageHandlers() pgoutput.Handler {
	// NB: pgx relation sets map a postgres schema to a pgx Message
	set := pgoutput.NewRelationSet(i.db.ConnInfo)

	handler := func(m pgoutput.Message, messageWalPos uint64) error {
		switch v := m.(type) {
		case pgoutput.Relation:
			// We have to add the Relations to our Set so that we can
			// decode our own output
			set.Add(v)
		case pgoutput.Insert:
			values, err := set.Values(v.RelationID, v.Row)
			if err != nil {
				return cerrors.Errorf("handleInsert failed: %w", err)
			}
			return i.handleInsert(v.RelationID, values, messageWalPos)
		case pgoutput.Update:
			values, err := set.Values(v.RelationID, v.Row)
			if err != nil {
				return cerrors.Errorf("handleUpdate failed: %w", err)
			}
			return i.handleUpdate(v.RelationID, values, messageWalPos)
		case pgoutput.Delete:
			values, err := set.Values(v.RelationID, v.Row)
			if err != nil {
				return cerrors.Errorf("handleDelete failed: %w", err)
			}
			return i.handleDelete(v.RelationID, values, messageWalPos)
		}
		return nil
	}

	return handler
}

// configureKeyColumn queries the db for the name of the primary key column
// for a table if one exists and sets it to the internal list.
// * TODO: Determine if tables must have keys
func (i *Iterator) configureKeyColumn() error {
	if i.config.KeyColumnName != "" {
		return nil
	}

	row := i.db.QueryRow(`SELECT column_name
		FROM information_schema.key_column_usage
		WHERE table_name = $1 AND constraint_name LIKE '%_pkey'
		LIMIT 1;`, i.config.TableName)

	var colName string
	err := row.Scan(&colName)
	if err != nil {
		return cerrors.Errorf("failed to scan row: %w", err)
	}

	if colName == "" {
		return cerrors.Errorf("got empty key column")
	}
	i.config.KeyColumnName = colName

	return nil
}

// configureColumns sets the default config to include all of the table's columns
// unless otherwise specified.
// * If other columns are specified, it uses them instead.
func (i *Iterator) configureColumns() error {
	if len(i.config.Columns) > 0 {
		return nil
	}

	query := fmt.Sprintf(`SELECT column_name 
		FROM INFORMATION_SCHEMA.COLUMNS 
		WHERE table_name = '%s'`, i.config.TableName)
	rows, err := i.db.Query(query)
	if err != nil {
		return cerrors.Errorf("withColumns query failed: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var val *string
		err := rows.Scan(&val)
		if err != nil {
			return cerrors.Errorf("failed to get column names from values: ")
		}
		i.config.Columns = append(i.config.Columns, *val)
	}

	return nil
}

func (i *Iterator) terminateBackend() error {
	rows, err := i.db.Query(fmt.Sprintf(
		`select pg_terminate_backend(active_pid)
		from pg_replication_slots
		where slot_name = '%s';`,
		i.config.SlotName))
	if err != nil {
		return cerrors.Errorf("failed to terminate replication slot: %w", err)
	}
	defer rows.Close()
	return nil
}

func getReplicationConnection(url string) (*pgx.ReplicationConn, error) {
	connInfo, err := pgx.ParseConnectionString(url)
	if err != nil {
		return nil, cerrors.Errorf("failed to parse connection info: %w", err)
	}
	replConn, err := pgx.ReplicationConnect(connInfo)
	if err != nil {
		return nil, cerrors.Errorf("failed to create replication connection: %w", err)
	}
	return replConn, nil
}

func parsePosition(pos string) (uint64, error) {
	n, err := strconv.ParseUint(pos, 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (i *Iterator) dropReplicationSlot() error {
	rows, err := i.db.Query(fmt.Sprintf(
		`select pg_drop_replication_slot(slot_name)
		from pg_replication_slots
		where slot_name = '%s;`, i.config.SlotName))
	if err != nil {
		return cerrors.Errorf("failed to drop replication slot: %w", err)
	}
	defer rows.Close()
	return nil
}

func (i *Iterator) dropPublication() error {
	query := fmt.Sprintf("drop publication if exists %s",
		i.config.PublicationName)
	rows, err := i.db.Query(query)
	if err != nil {
		return cerrors.Errorf("failed to connecto to replication: %w", err)
	}
	defer rows.Close()
	return nil
}
