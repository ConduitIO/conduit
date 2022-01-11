Conduit PostgreSQL Connectors 
============================

# Source
The Postgres Source Connector connects to a database with the provided `url` and
then will call `Ping` and test the connection. If the `Ping` fails, the `Open`
method will fail and the Connector will not be ready to Read. 

## Change Data Capture
This connector implements CDC features for PostgreSQL by reading WAL log events 
into a buffer that is checked on each Read request after the initial table Rows 
have been read.

## CDC  Configuration
When Open is called, the connector will attempt to start all necessary 
connections and will run some initial setup commands to create the logical 
replication slots it needs to run and start its own subscription. 

The connector user specified in the connection URL must have sufficient 
privileges to run all of these commands or it will fail.

If the `cdc` field in the plugin.Configuration is set to any truthy value, it 
will be enabled. 

Publication and slot name are user configurable, and must be correctly set. 
The plugin will do what it can to be smart about publication and slot 
management, but it can't handle everything.

If a replication_url is provided, it will be used for CDC features instead of 
the url. If no replication_url is provided, but cdc is enabled, then it will 
attempt to use that url value for CDC features and logical replication setup.

Example configuration for CDC features:
```
"cdc":              "true",
"publication_name": "meroxademo",
"slot_name":        "meroxademo",
"url":              url,
"replication_url":  url,
"table":            "records",
"columns":          "key,column1,column2,column3",
```

### Internal Event Buffer
There is a private variable bufferSize that dictates the size of the channel 
buffer that holds WAL events. If it's full, pushing to that channel will be a 
blocking operation, and thus execution will stop if the handler for WAL events 
cannot push into that buffer. That blocking execution could have unknown 
negative performance consequences, so we should have this be sufficiently high 
and possibly configured by environment variable.
WAL Events

In all cases we should aim for consistency between WAL event records and 
standard Row records, so any discrepancies between the formats of those should 
be pointed out. I've addressed some where I've found them, for example the 
integer handling in `withValues` function treats all numbers as `int64` since 
that's how the row queries are handled as well.

### Go and PostgreSQL Data Types
We handle the basic Postgres types for now, but we will need an exhaustive test 
suite of all the different data types that Postgres can handle.

### Position Handling
The WAL uses Postgres' internal LSN (log sequence number) to track positions of 
WAL events, but they're not an exact science, and they are subject to 
wrap-around at their high end limit. This means that once the plugin is reading 
only WAL events, the LSN will likely be orders of magnitude higher than the row
Position of the last read row. This could cause some issues, but can still be 
handled by the Connector setting Position back to 0 and reading the table from 
the start again. 

However, that isn't the greatest long-term solution and we should handle this by
adding a highwater mark for the last read Row in our database query.


## Position 
The position argument let's the connector know what the _current_ position of
the Read is at. When you call Read, it takes that _current_ position and 
attempts to get the next one. Because of this, we attempt to parse the Position 
as an integer and then increment it.

The read query will perform a lookup for a row where `key >= $1` where key is 
the name of the key column and $1 is the value of the incremented Position.
`plugins.ErrEndData` is returned if no row can be found that matches this 
selection.

## Record Keys
Position currently references the `key` column when querying. 
Thus, `key` must be a column of integer, serial or bigserial type, or be a
string that can be parsed as an integer.

Positions are parsed to integers with `ParseInt` and then parsed back to strings
with `FormatInt`. `incrementPosition` and `withPosition` respectively handle 
this logic.

If no `key` field is provided, then the connector will attempt to look up the 
primary key column of the table. If that can't be determined it will error.

## Configuration 
The config passed to `Open` can be contain the following fields.

| name             | description                                                                                                                     | required             |
|------------------|---------------------------------------------------------------------------------------------------------------------------------|----------------------|
| table            | the name of the table in Postgres that the connector should read                                                                | yes                  |
| url              | formatted connection string to the database.                                                                                    | yes                  |
| columns          | comma separated string list of column names that should be built in to each Record's payload.                                   | no                   |
| key              | column name that records should use for their `Key` fields. defaults to the column's primary key if nothing is specified        | no                   |
| cdc              | enables CDC features                                                                                                            | req. for CDC mode    |
| publication_name | name of the publication to listen for WAL events                                                                                | req.  for CDC mode   |
| slot_name        | name of the slot opened for replication events                                                                                  | req.  for CDC mode   |
| replication_url  | URL for the CDC connection to use. If no replication_url is provided, then the CDC connection attempts the use the `url` value. | optional in CDC mode |

## Columns
If no column names are provided in the config, then the plugin will assume 
that all columns in the table should be queried. It will attempt to get the 
column names for the configured table and set them in memory.

## Record Keys 
Currently the Postgres source takes a `key` property and uses that column name
to key records. The key column must be unique and must be able to be parsed 
as an integer. 

We plan to support alphanumeric and composite record keys soon.

# Roadmap 
- [x] Change data capture handling
- [ ] Composite key support 
- [ ] Alphanumeric position handling 
- [ ] JSONB support

# Testing 
Run the docker-compose from the project root:
```bash
docker-compose -f ./test/docker-compose-postgres.yml up
```

Run all connector tests:
```bash
go test -race ./pkg/plugins/pg/...
```

# References 
- https://github.com/batchcorp/pgoutput
- https://github.com/bitnami/bitnami-docker-postgresql-repmgr