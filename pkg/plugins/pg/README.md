Conduit PostgreSQL Connectors 
=============================

Source
======
The Postgres Source Connector connects to a database with the provided `url` and
then will call `Ping` to test the connection. If the `Ping` fails, the `Open`
method will fail and the Connector won't be started.

Upon starting, the source takes a snapshot of a given table in the database, 
then switches into CDC mode. In CDC mode, the plugin reads from a buffer of 
CDC events.

## Snapshot Capture
When the connector first starts, snpashot mode is enabled. The connector
acquires a read-only lock on the table, and then reads all rows of the table 
into Conduit. Once all of the rows in that initial snapshot are read, then the 
connector releases it's lock and switches into CDC mode. 

This behavior is enabled by default, but can be turned off by adding 
`"snapshot":"off"` to the Source configuration.

## Change Data Capture
This connector implements CDC features for PostgreSQL by reading WAL events 
into a buffer that is checked on each call of `Read` after the initial snapshot
has occurred. If there is a record in the buffer, it grabs and returns that 
record. If it's empty, it returns `ErrEndData` signaling that Conduit should 
backoff-retry.

 This behavior is enabled by default, but can be turned off by adding 
 `"cdc": "off"` to the Source configuration.

### CDC  Configuration
When the connector switches to CDC mode, it attempts to start all necessary 
connections and runs the initial setup commands to create its logical 
replication slots and publications. It will connect to an existing slot if one
with the configured name exists.

The Postgres user specified in the connection URL must have sufficient 
privileges to run all of these setup commands or it will fail.

Publication and slot name are user configurable, and must be correctly set. 
The plugin will do what it can to be smart about publication and slot 
management, but it can't handle everything.

If a `replication_url` is provided, it will be used for CDC features instead of 
the url. If no `replication_url` is provided, but cdc is enabled, then it will 
attempt to use that url value for CDC features and logical replication setup.
Example configuration for CDC features:
```
"cdc":              "true",
"publication_name": "meroxademo",
"slot_name":        "meroxademo",
"url":              url, // connection url to the database
"replication_url":  url,
"key":              "key", // postgres column name of your table's primary key
"table":            "records", // table that the connector should watch
"columns":          "key,column1,column2,column3", // columns to include in payload
```

### CDC Event Buffer
There is a private variable bufferSize that dictates the size of the channel 
buffer that holds WAL events. If it's full, pushing to that channel will be a 
blocking operation, and thus execution will stop if the handler for WAL events 
cannot push into that buffer. That blocking execution could have unknown 
negative performance consequences, so we should have this be sufficiently high 
and possibly configured by environment variable.

## Key Handling
If no `key` field is provided, then the connector will attempt to look up the 
primary key column of the table. If that can't be determined it will error.

## Columns
If no column names are provided in the config, then the plugin will assume 
that all columns in the table should be returned. It will attempt to get the 
column names for the configured table and set them in memory.

## Configuration Options

| name             | description                                                                                                                     | required             | default              |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------- | -------------------- | -------------------- |
| table            | the name of the table in Postgres that the connector should read                                                                | yes                  | n/a                  |
| url              | formatted connection string to the database.                                                                                    | yes                  | n/a                  |
| columns          | comma separated string list of column names that should be built in to each Record's payload.                                   | no                   | `*` (all columns)    |
| key              | column name that records should use for their `Key` fields. defaults to the column's primary key if nothing is specified        | no                   | primary key of table |
| snapshot         | whether or not the plugin will take a snapshot of the entire table acquiring a read level lock before starting cdc mode         | n/a                  | enabled              |
| cdc              | enables CDC features                                                                                                            | req. for CDC mode    | off                  |
| publication_name | name of the publication to listen for WAL events                                                                                | req. for CDC mode    | `pglogrepl`          |
| slot_name        | name of the slot opened for replication events                                                                                  | req. for CDC mode    | `pglogrepl_demo`     |
| replication_url  | URL for the CDC connection to use. If no replication_url is provided, then the CDC connection attempts the use the `url` value. | optional in CDC mode | n/a                  |

Destination 
===========
The Postgres Destination takes a `record.Record` and parses it into a valid 
SQL query. The Destination is designed to handle different payloads and keys.
decause of this, each record is individually parsed and upserted. 

## Table Name
Every record must have a `table` property set in it's metadata, otherwise it
will error out. However, because of this, our Destination write can support 
multiple tables in the same connector provided the user has proper access to 
those tables.

## Keys
Keys in the Destination are optional and must be unique if they are set.

If a Key is included in a Payload, it will be removed.  This is because the Key 
is also inserted into the database, so the Payload removes it. 

This means a Payload value will be ignored if it's also the Key value.

### Upsert Behavior
If there is a conflict on a Key, the Destination will upsert with its current 
received values. Because Keys must be unique, this can overwrite and thus 
potentially lose data, so keys should be assigned correctly from the Source.

## Configuration Options

| name | description                                  | required | default |
| ---- | -------------------------------------------- | -------- | ------- |
| url  | the connection URI for the Postgres database | yes      | n/a     |

# Testing 
If you're running the integration tests, you'll need a Postgres database with 
replication enabled. You can use our docker-compose file that works with the 
default test settings by running:

```bash
docker-compose -f ./test/docker-compose-postgres.yml up -d
```

*Note*: remove the -d flag from either docker-compose command to hold the
container connection open and watch its logs.

Once the docker-compose services are running, you can run the integration tests:

```bash
go test -race -v -timeout 10s --tags=integration ./pkg/plugins/pg/...
```

Run all connector unit tests:
```bash
go test -race -v -timeout 10s ./pkg/plugins/pg/...
```

# References 
- https://github.com/batchcorp/pgoutput 
- https://github.com/bitnami/bitnami-docker-postgresql-repmgr
- https://github.com/Masterminds/squirrel"