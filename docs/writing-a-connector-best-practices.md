The following are some high-level guidelines when writing a connector.

## Start with the connector template

1. Can be found at https://github.com/ConduitIO/conduit-connector-template/
2. The template provides a basic project setup that includes placeholders for
   the source connector, destination connector code, connector specifications,
   example of a configuration etc.
3. The template also sets up commonly used GitHub workflow for testing, linting,
   etc.

## Research the 3rd party system

Some questions that typically need to be answered:

1. How is the data organized in the system (buckets, tables, collections,
   indexes, etc.)? What's the hierarchy?
2. How can we connect to the system? Connection strings/URIs are preferred.
3. What drivers are available? Pure Go drivers are preferred.
4. What authentications methods are supported (basic auth, TLS, tokens, etc.)?
   Clarify which auth. methods need to be supported in the driver.
5. Investigate how can the connector read/write to the source without affecting
   other clients (e.g. when getting messages from a message broker's queue, the
   message will be delivered to the connector, while other clients might expect
   the message).

## Development

### Test pipeline

When working on a source connector, a file or log destination can be used to
build a test pipeline. When working on a destination, a file or generator source
can be used.

### Middleware

Conduit's Connector SDK adds default middleware that, by default, enables some
functionality. A connector developer should be familiar with the middleware.

### Source

#### Snapshot

Investigate how snapshots (i.e. pulling all the existing data can be done).
Clarify if that's actually possible or a requirement for the connector (in some
situations it can be quite complex).

In snapshots should be supported by the source connector, make sure to implement
consistent snapshoting. An example of that can be found in the Postgres
connector.

#### Change Data Capture (CDC)

1. In a snapshot is needed, make sure to capture changes that happened while the
   snapshot is running.
2. Investigate how to support different types of changes: creates, updates,
   deletes.
3. For example, for some RDBMs (Postgres, MySQL) there's a changelog (
   WAL/binlog). In some RDBMs, triggers can be used.
4. If there's no native way in a 3rd party system to get changes, a timestamp
   based query can be used.

### Destination

#### Batching

Batching can considerably improve the write performance. The Connector SDK
provides batching middleware. A destination connector should take advantage of
this if possible.

### Debugging the connector

https://github.com/ConduitIO/conduit/discussions/1724#discussioncomment-10178484