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
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/batchcorp/pgoutput"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/jackc/pgx"
)

var bufferSize = 1000

// withCDC sets up change data capture for the Postgres Source or returns an
// error.
func (s *Source) withCDC(cfg plugins.Config) error {
	c := context.Background()
	ctx, cancel := context.WithCancel(c)
	s.killswitch = cancel
	// early return if cdc is disabled
	v, ok := cfg.Settings["cdc"]
	// if cdc is set, check the value for falsy values.
	if ok {
		switch v {
		case "disabled", "false", "off", "0":
			log.Println("cdc behavior turned off")
			return nil
		}
	}
	// setup a WaitGroup to track our Postgres subscription's goroutine
	s.subWG = sync.WaitGroup{}
	// uri is the string used to connect the CDC subscription to Postgres
	uri := getURI(cfg)
	// slotName is the name of the slot that we're occupying
	// if the slot doesn't exist it will be created.
	slotName := getSlotName(cfg)
	// the name of the publication that we're consuming.
	// if this doesn't exist it will be created.
	publication := getPublicationName(cfg)

	// create a buffered channel of bufferSize for WAL events and make a new
	// *CDCIterator with it.
	// * this must be a buffered channel or else it will block the handler
	// on reads and events won't get processed
	buf := make(chan record.Record, bufferSize)
	s.cdc = NewIterator(buf)

	// check for the existence of a table field, error if it's not set
	table, ok := cfg.Settings["table"]
	if !ok {
		return cerrors.New("withCDC error: must provide a table name")
	}

	// parse the uri to get a connection config from the URL of the database
	connConfig, err := pgx.ParseURI(uri)
	if err != nil {
		return cerrors.Errorf("failed to parse connection url: %w", err)
	}

	// connect to the replication with the given config
	conn, err := pgx.ReplicationConnect(connConfig)
	if err != nil {
		return cerrors.Errorf("pgx failed to connect to replication: %w", err)
	}
	log.Printf("connected to replication stream: %+v", conn.ConnInfo)

	// create the publication for the given table
	log.Printf("attempting to setup publication %s for table %s", publication, table)
	_, err = conn.Exec(fmt.Sprintf("CREATE PUBLICATION %s FOR TABLE %s;", publication, table))
	if err != nil {
		if !strings.Contains(err.Error(), "SQLSTATE 42710") {
			return cerrors.Errorf("failed to create publication %s: %w", slotName, err)
		}
		log.Printf("publication %s already exists; continuing to slot setup", publication)
	}

	// create the replication slot
	err = conn.CreateReplicationSlot(slotName, "pgoutput")
	if err != nil {
		// detect if it's an SQL 42710 'already exists' error
		// if it's not, return that error
		if !strings.Contains(err.Error(), "SQLSTATE 42710") {
			return cerrors.Errorf("failed to create replication slot: %w", err)
		}
		log.Printf("replication slot %s already exists - continuing startup", slotName)
	}

	// make a new relation set to track relations through recv'd message
	set := pgoutput.NewRelationSet(conn.ConnInfo)

	// declare our message handler for each message we receive from postgres.
	// * this receives a Message and that Message's WAL position.
	// https://github.com/batchcorp/pgoutput/commit/54ebe1782ab770d6f706c2f0e53335cbe2f2fee0
	handler := func(m pgoutput.Message, pos uint64) error {
		switch v := m.(type) {
		case pgoutput.Relation:
			// We have to add the Relations to our Set so that we can
			// decode our own output
			set.Add(v)
		case pgoutput.Insert:
			values, err := set.Values(v.RelationID, v.Row)
			if err != nil {
				return cerrors.Errorf("handleInsert failed to get values: %w", err)
			}
			return s.handleInsert(v.RelationID, values, pos)
		case pgoutput.Update:
			values, err := set.Values(v.RelationID, v.Row)
			if err != nil {
				return cerrors.Errorf("handleUpdate failed to get values: %w", err)
			}
			return s.handleUpdate(v.RelationID, values, pos)
		case pgoutput.Delete:
			values, err := set.Values(v.RelationID, v.Row)
			if err != nil {
				return cerrors.Errorf("handleDelete failed to get values: %w", err)
			}
			return s.handleDelete(v.RelationID, values, pos)
		}
		return nil
	}

	// create a new subscription and save its reference
	sub := pgoutput.NewSubscription(conn, slotName, publication, 0, false)
	s.sub = sub

	// start the subscription or return an error
	s.subWG.Add(1)
	go func() {
		defer func() {
			s.sub = nil
			log.Printf("cleaning up replication subscription")
			cancel()
			s.subWG.Done()
		}()
		log.Printf("starting up subscription for %s", slotName)
		// NB: Start holds execution until it errors or the context is canceled
		err := s.sub.Start(ctx, 0, handler)
		if err != nil {
			if err == context.Canceled {
				// if the error is a context cancellation, don't assign the
				// error because we consider this correct handling.
				log.Println("context cancellation detected - returning")
				return
			}
			s.subErr = cerrors.Errorf("postgres subscription produced an error: %w", err)
		}
	}()

	return nil
}

func getPublicationName(cfg plugins.Config) string {
	pub, ok := cfg.Settings["publication_name"]
	if !ok {
		return "pglogrepl"
	}
	return pub
}

func getSlotName(cfg plugins.Config) string {
	name, ok := cfg.Settings["slot_name"]
	if !ok {
		return "pglogrepl_demo"
	}
	return name
}

func getURI(cfg plugins.Config) string {
	var uri string
	// check for default url and set it to uri if it exists
	url, ok := cfg.Settings["url"]
	if ok {
		uri = url
	}

	// if a replication_url field is set, use that instead of the default url
	replURL, ok := cfg.Settings["replication_url"]
	if ok {
		uri = replURL
	}

	return uri
}
