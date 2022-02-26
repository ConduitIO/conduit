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
	"log"
	"strings"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/plugin/sdk"

	"github.com/batchcorp/pgoutput"
	"github.com/jackc/pgx"
)

// cdcBufferSize is the size of the message buffer created for cdc messages.
// If this fills up it will block on calls to Source#Read.
const cdcBufferSize int = 1000

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

	// startingLSN uint64
}

// Iterator listens for events from the WAL and pushes them into its buffer.
// It iterates through that Buffer so that we have a controlled way to get 1
// record from our CDC buffer without having to expose a loop to the main Read.
type Iterator struct {
	wg *sync.WaitGroup

	config Config

	lsn          uint64 // TODO: or maybe use an LSN type here
	messages     chan sdk.Record
	errCollector chan error
	killswitch   context.CancelFunc

	conn *pgx.ReplicationConn
	sub  *pgoutput.Subscription
}

// NewCDCIterator takes a config and returns up a new CDCIterator or returns an
// error.
func NewCDCIterator(ctx context.Context, config Config) (*Iterator, error) {
	wctx, cancel := context.WithCancel(ctx)
	i := &Iterator{
		config:       config,
		messages:     make(chan sdk.Record, cdcBufferSize),
		wg:           &sync.WaitGroup{},
		errCollector: make(chan error),
		killswitch:   cancel,
	}

	go func() {
		// TODO: Not sure if error collection like this is best
		// or if we should close the thread and error out
		for err := range i.errCollector {
			log.Printf("[ERROR]: %+v", err)
		}
	}()

	err := i.connect()
	if err != nil {
		return nil, cerrors.Errorf("failed to connect: %v", err)
	}

	err = i.withColumns()
	if err != nil {
		return nil, cerrors.Errorf("failed to find table columns: %v", err)
	}

	err = i.withKeyColumn()
	if err != nil {
		return nil, cerrors.Errorf("failed to find key: %w", err)
	}

	err = i.setupSubscription()
	if err != nil {
		return nil, cerrors.Errorf("failed to setup subscription %w", err)
	}

	err = i.setPosition()
	if err != nil {
		return nil, cerrors.Errorf("failed to set starting position: %w", err)
	}

	err = i.subscribe(wctx)
	if err != nil {
		return nil, cerrors.Errorf("failed to start cdc subscription: %w", err)
	}

	return i, nil
}

func (i *Iterator) setPosition() error {
	log.Printf("no position configured - defaulting to confirmed flush LSN")
	err := i.setToFlushLSN()
	if err != nil {
		return cerrors.Errorf("failed to set default flush lsn: %w", err)
	}

	return nil
}

func (i *Iterator) setToFlushLSN() error {
	rows, err := i.conn.Query("select restart_lsn, confirmed_flush_lsn from pg_catalog.pg_replication_slots where slot_name = $1;", i.config.SlotName)
	if err != nil {
		return cerrors.Errorf("set to flush failed to find a default: %w", err)
	}
	defer rows.Close()

	if rows.Err() != nil {
		log.Printf("rows error: %v", err)
	}
	var restartLSN *string
	if !rows.Next() {
		return cerrors.Errorf("failed to find lsn for slot %w: ", err)
	}

	err = rows.Scan(&restartLSN)
	if err != nil {
		return cerrors.Errorf("failed to scan restart_lsn: %w", err)
	}

	return nil
}

// HasNext returns true if there is an item in the buffer.
func (i *Iterator) HasNext() bool {
	return len(i.messages) > 0
}

// Next returns the next record in the buffer. This is a blocking operation
// so it should only be called if we've checked that HasNext is true or else
// it will block until a record is inserted into the queue.
func (i *Iterator) Next() (sdk.Record, error) {
	r := <-i.messages
	return r, nil
}

// Push pushes a record into the buffer.
func (i *Iterator) Push(r sdk.Record) {
	i.messages <- r
}

// Teardown will kill the CDC subscription and wait for it to be done, then
// closes its connection to the database and cleans up connector slots and
// publications.
func (i *Iterator) Teardown() error {
	log.Printf("tearing down postgres cdc connector")
	i.killswitch()
	i.wg.Wait()
	cleanupErr := i.cleanupConnector()
	dbErr := i.conn.Close()
	return multierror.Append(cleanupErr, dbErr)
}

func (i *Iterator) setupSubscription() error {
	if i.config.PublicationName == "" {
		i.config.PublicationName = "conduitpub" // TODO: update these default values in the spec
	}
	if i.config.SlotName == "" {
		i.config.SlotName = "conduitslot" // TODO: update these defaults in the spec
	}

	err := i.connect()
	if err != nil {
		return cerrors.Errorf("failed to connecto to replication: %w", err)
	}

	err = i.createPublicationForTable()
	if err != nil {
		return cerrors.Errorf("failed to create publication: %w", err)
	}

	err = i.createReplicationSlot()
	if err != nil {
		return cerrors.Errorf("failed to create replication slot: %w", err)
	}

	var maxWalRetain uint64 = 0 // TODO: see if we need to set walRetain to anything bigger than 0
	var failOnHandler bool = false
	i.sub = pgoutput.NewSubscription(
		i.conn,
		i.config.SlotName,
		i.config.PublicationName,
		maxWalRetain,
		failOnHandler)

	return nil
}

func (i *Iterator) createReplicationSlot() error {
	err := i.conn.CreateReplicationSlot(i.config.SlotName, "pgoutput")
	if err != nil {
		if !strings.Contains(err.Error(), "SQLSTATE 42710") {
			return cerrors.Errorf("failed to create replication slot: %v", err)
		}
		log.Printf("replication slot %s already exists - continuing startup",
			i.config.SlotName)
	}
	return nil
}

// createPublicationForTable ...
func (i *Iterator) createPublicationForTable() error {
	log.Printf("attempting to setup publication %s for table %s;",
		i.config.PublicationName,
		i.config.TableName)
	_, err := i.conn.Exec(
		fmt.Sprintf("create publication %s for table %s;",
			i.config.PublicationName,
			i.config.TableName))
	if err != nil {
		if !strings.Contains(err.Error(), "SQLSTATE 42710") {
			return cerrors.Errorf("failed to create publication %s: %w",
				i.config.SlotName, err)
		}
		log.Printf("publication %s already exists - continuing to slot setup",
			i.config.PublicationName)
	}
	return nil
}

func (i *Iterator) connect() error {
	pgConnInfo, err := pgx.ParseURI(i.config.URL)
	if err != nil {
		return cerrors.Errorf("pgx failed to parse uri: %w", err)
	}
	conn, err := pgx.ReplicationConnect(pgConnInfo)
	if err != nil {
		return cerrors.Errorf("pgx failed to connect to replication: %w", err)
	}
	i.conn = conn
	return nil
}

// registerMessageHandlers returns a Handler for attaching to each message type.
func (i *Iterator) registerMessageHandlers() pgoutput.Handler {
	// pgx relation sets relate a schema to a pgx message
	set := pgoutput.NewRelationSet(i.conn.ConnInfo)

	// declare our message handler for each Message we receive from postgres.
	// * this receives a Message and that Message's WAL position.
	// https://github.com/batchcorp/pgoutput/commit/54ebe1782ab770d6f706c2f0e53335cbe2f2fee0
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

// subscribe starts the subscription at the configured LSN in a goroutine and
// then returns any errors that occurred.
// * The waitgroup on Iterator waits on the goroutine created here at teardown.
func (i *Iterator) subscribe(ctx context.Context) error {
	var walRetain uint64 = 0
	sub := pgoutput.NewSubscription(
		i.conn,
		i.config.SlotName,
		i.config.PublicationName,
		walRetain,
		false,
	)
	i.sub = sub
	i.wg.Add(1)
	go func() {
		defer func() {
			log.Printf("cdc subscription closed")
			i.wg.Done()
		}()

		err := i.sub.Start(ctx, i.lsn, i.registerMessageHandlers())
		if err != nil {
			if err == context.Canceled {
				return
			}
			i.errCollector <- err
		}
	}()
	return nil
}

// // getFlushLSN ...
// func (i *Iterator) getFlushLSN() (string, error) {
// 	query := `select confirmed_flush_lsn
// 		from pg_catalog.pg_replication_slots
// 		where slot_name = 'conduitslot'`

// 	rows, err := i.conn.Query(query)
// 	if err != nil {
// 		return "", cerrors.Errorf("failed to query for flush_lsn: %w", err)
// 	}
// 	defer rows.Close()

// 	if !rows.Next() {
// 		return "", cerrors.Errorf("failed to find slot with name %s: %w",
// 			i.config.SlotName, err)
// 	}

// 	var flushLSN *string
// 	if err := rows.Scan(&flushLSN); err != nil {
// 		return "", cerrors.Errorf("failed to scan lsn position: %w", err)
// 	}

// 	return *flushLSN, nil
// }

// withKeyColumn queries the db for the name of the primary key column
// for a table if one exists and sets it to the internal list.
// * TODO: Determine if tables must have keys
func (i *Iterator) withKeyColumn() error {
	if i.config.KeyColumnName != "" {
		log.Printf("keying records with row %s", i.config.KeyColumnName)
		return nil
	}

	row := i.conn.QueryRow(`SELECT column_name
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

// withColumns sets the default config to include all of the table's columns
// unless otherwise specified.
// * If other columns are specified, it uses them instead.
func (i *Iterator) withColumns() error {
	if len(i.config.Columns) > 0 {
		log.Printf("watching %v from %s", i.config.Columns, i.config.TableName)
		return nil
	}

	query := fmt.Sprintf(`SELECT column_name 
		FROM INFORMATION_SCHEMA.COLUMNS 
		WHERE table_name = '%s'`, i.config.TableName)

	rows, err := i.conn.Query(query)
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

	log.Printf("setting source to read columns [%+v]", i.config.Columns)

	return nil
}

func (i *Iterator) cleanupConnector() error {
	termErr := i.terminateBackend()
	dropReplErr := i.dropReplicationSlot()
	dropPubErr := i.dropPublication()

	return multierror.Append(
		termErr,
		dropReplErr,
		dropPubErr,
	)
}

func (i *Iterator) terminateBackend() error {
	rows, err := i.conn.Query(`select pg_terminate_backend(active_pid) 
		from pg_replication_slots
		where slot_name = $1;`, i.config.SlotName)
	if err != nil {
		return cerrors.Errorf("failed to terminate replication slot: %w", err)
	}
	defer rows.Close()
	return nil
}

func (i *Iterator) dropReplicationSlot() error {
	rows, err := i.conn.Query(`select pg_drop_replication_slot(slot_name) 
		from pg_replication_slots
		where slot_name = $1;`, i.config.SlotName)
	if err != nil {
		return cerrors.Errorf("failed to drop replication slot: %w", err)
	}
	defer rows.Close()
	return nil
}

func (i *Iterator) dropPublication() error {
	query := fmt.Sprintf("drop publication %s", i.config.PublicationName)
	rows, err := i.conn.Query(query)
	if err != nil {
		return cerrors.Errorf("failed to connecto to replication: %w", err)
	}
	defer rows.Close()
	return nil
}
