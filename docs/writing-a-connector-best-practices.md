Certain patterns have proven useful when writing connectors. Over the past
couple of years we've refined some of those and also improved our process for
writing connectors. The result are guidelines for writing a new Conduit
connector.

## Start with the connector template

The GitHub
repository [template](https://github.com/ConduitIO/conduit-connector-template/)
for Conduit connectors provides a basic project setup that includes placeholders
for the source connector code, destination connector code, connector
specifications, example of a configuration etc.

Also included is a `Makefile` with commonly used target, GitHub workflow for
linting the code, running unit tests, automatically merging minor dependabot
upgrades, etc.

## Research the 3rd party system

Researching the 3rd party system for which a Conduit connector is built helps
understand the capabilities and limitations of the system, which in turn results
in a better connector design and better work organization.

Some questions that typically need to be answered:

1. **How is the data organized in the system?**

   The way data is organized in the system will affect how the connector is
   configured (e.g. what data is a user able to read or write to) and how the
   hierarchy is mapped to records and collections in Conduit.
2. **What parameters are needed to connect to the system?**

   We generally recommend using connection strings/URIs, if available. The
   reason for this is that modifying a connector's parameters is a matter of
   changing an existing string, and not separate configuration parameters in a
   connector. 
3. **What APIs or drivers are available?**

   If a public API **and** a driver are available, we recommend using a driver,
   since it's a higher level of abstraction. However, the choice is influenced
   by other factors, such as:
   - In which language should the connector be written?
   - If the language of choice is Go, is a pure Go driver available? A CGo
     driver may make the usage and deployment of a connector more complex.
   - Does the driver expose all the functionality needed?
   - How mature is the driver?
4. **What authentication methods should be supported?**

   If multiple authentication methods are available, then a decision needs to be
   made about the methods that should be implemented first. Additionally, it
   should be understood how expired credentials should be handled. For example,
   a connector won't be able to handle an expired password, but a token can
   sometimes be refreshed. 
5. Investigate how can the connector read/write to the source without affecting
   other clients (e.g. when getting messages from a message broker's queue, the
   message will be delivered to the connector, while other clients might expect
   the message).
6. How to perform a snapshot?
7. How to perform change data capture (CDC)?
8. How to resume reading from a source system?
6. **Can the 3rd party system be run as a containerized application?**

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

#### Iterator pattern

TBD

### Destination

#### Batching

Batching can considerably improve the write performance. The Connector SDK
provides batching middleware. A destination connector should take advantage of
this if possible.

### Debugging the connector

https://github.com/ConduitIO/conduit/discussions/1724#discussioncomment-10178484