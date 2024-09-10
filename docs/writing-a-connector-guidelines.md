# Guidelines for writing a connector

This document describes patterns and processes that have been successfully used
in many Conduit connectors over the past few years. The document assumes basic
knowledge about how Conduit works.

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

   The choice is influenced by a number of factors, such as:
   - Is there a driver available? If a public API and a driver are available, we
     recommend using a driver, since it's a higher level of abstraction.
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
5. **Can the connector be isolated from other clients of the system?**

   In some cases a connector, as a client using a system, might affect other
   clients, for example when getting messages from a message broker's queue, the
   message will be delivered to the connector, while other clients might expect
   the message.
6. **Are the source/destination specific features supported?**

   Source and destination connectors may have specific requirements (some of
   them are outlined in later sections). When researching, attention should be
   paid if those requirements can be fulfilled.

## Development

## Familiarize with the Connector SDK/connector protocol

Conduit's Connector SDK is the Go software development kit for implementing a
connector for Conduit. If you want to implement a connector in another language
please have a look at
the [connector protocol](https://github.com/conduitio/conduit-connector-protocol).

### Write a test pipeline

Having a test pipeline helps see the results of development sooner rather than
later. When working on a source connector, a file or log destination can be used
to build a test pipeline. When working on a destination, a file or generator
source can be used to manually or automatically generate some test data for the
destination connector.

### Use built-in connectors as examples

Conduit's [built-in connectors](https://conduit.io/docs/connectors/connector-list/)
are great references that can be used when writing connectors. They are also the
first ones to be updated with latest SDK and protocol changes.

### Configuration

A connector's configuration is a map in which keys are parameter names and
values are parameter values. Every source and destination needs to return the
parameters that it accepts, and, optionally, the validations that need to be run
on those.

Conduit
offers [ParamGen](https://github.com/ConduitIO/conduit-commons/tree/main/paramgen),
a tool that generates the parameters map from a configuration struct. The SDK
contains a function, `sdk.Util.ParseConfig`, that can parse and validate a
user's configuration map into the configuration struct.

### Middleware

Conduit's Connector SDK adds default middleware that, by default, enables some
functionality. A connector developer should be familiar with the middleware,
especially with
the [schema related middleware](https://conduit.io/docs/connectors/configuration-parameters/schema-extraction).

### Source connectors

The following section summarizes best practices that are specific to source
connectors.

#### Deciding how should a record position look like

Every record read by a source connector has
a [position](https://conduit.io/docs/features/opencdc-record#fields) attached.
If a connector is stopped after that record is read, its position will be given
to the connector's `Open()` method the next time it starts. Hence, a position
needs to contain enough information for a source connector to resume reading
records from where it exactly stopped.

A position is a slice of bytes that can represent any data structure. In Conduit
connectors, it's common to see that a position is actually a `struct`, that's
marshalled into a JSON string.

#### Implementing snapshots

Firstly, it should be clarified if supporting snapshots is a requirement or if
it's possible to do at all. If a connector is required to support snapshots,
then it's recommended to make it possible to turn off snapshots through the
connector configuration.

The following things need to be taken into account when implementing a snapshot
procedure:

- The snapshot needs to be consistent.
- The set of the existing data can be quite large.
- Restarting a connector during a snapshot should **not** require re-reading all
  the data again. This is because it in some destination connectors it may cause
  data duplication, and it could be a significant performance overhead.

#### Implementing change data capture (CDC)

Change Data Capture (CDC) should be implemented so that the following criteria
is met:

1. If a snapshot is needed, changes that happened while the
   snapshot was running should be captured too.
2. Different types of changes might be possible (new data inserted, existing
   data updated or deleted).
3. Changes that happened while a connector was stopped need to be read by the
   connector when it starts up (assuming that the changes are still present in
   the source system).

Some source systems may provide a change log (e.g. WAL in PostgreSQL, binlog in
MySQL, etc.). Others may not and a different way to capture changes is needed.
Specifically, for connectors written for SQL databases, two patterns can be
used:

1. **Triggers**

   With triggers, it's possible to capture all types of changes (creates,
   updates and deletes). A trigger will write the event with necessary
   metadata (operation performed, timestamp, etc.) into a "trigger table", that
   will be read by the connector.

   An advantage of this approach is that all types of operations can be
   captures. However, it may incur a performance penalty.
2. **A timestamp-based query**

   If the table a source connector is reading from has a timestamp column that
   is updated whenever a new row is inserted or an existing row is updated, then
   a query can be used to fetch changes. A disadvantage of this approach is that
   delete operations cannot be captured.

#### Iterator pattern

A pattern that we found to be useful when writing source connectors is the
iterator pattern. The basic idea is that a source connector's `Read()` method
reads records through an iterator:

```go
type Iterator interface {
	HasNext() bool
	Next() opencdc.Record
}

type Source struct {}
func (s *Source) Read(ctx context.Context) (opencdc.Record, error) {
	if s.iterator.HasNext(ctx) {
        return opencdc.Record{}, sdk.ErrBackoffRetry
   }
       
    return s.iterator.Next(ctx)
}
```

There are three implementations of the iterator interface:

- `SnapshotIterator`: used when performing a snapshot
- `CDCIterator`: used in CDC
- `CombinedIterator`: an iterator that combines the above two and is able to
  perform a snapshot and then switch to CDC.

The iterator to be used is determined in the source's `Open()` method based on
the connector's configuration: if a snapshot is required, then a
`CombinedIterator` will be used, if not, then the `CDCIterator` will be used.

There are multiple advantages of this approach. The source connector remains "
focused" on the higher level operations, such as configuration, opening the
connectors, tearing down, etc. The iterators contain the code that deals with
the source system itself and convert the data into `opencdc.Record`s. They also
take care of switching from snapshot mode to CDC mode.

### Destination connectors

#### Batching

Batching can considerably improve the write performance and should be considered
when developing a destination connector. The Connector SDK
provides [batching middleware](https://conduit.io/docs/connectors/configuration-parameters/batching)
that automatically builds batches.

When writing batches into a destination system, the order of records should
**not** be changed, even if grouping records would be useful in a way. For
example, in some connectors, it's useful to group records by operation, because
the destination system has different procedures for those operations.

If a destination connector receives the following batch:

```text
[create_1, update_1, update_2, create_2]
```

then this batch can be split into following batches:

```text
Batch 1: [create_1]
Batch 2: [update_1, update_2]
Batch 3: [create_2]
```

This way, ordering is preserved and the connector can still take advantage of
batching.

### Acceptance tests

Conduit's Connector SDK contains a set of acceptance tests that verify many of a
connector's methods, such as:

- Are missing configuration parameters detected?
- Can a source that is stopped during CDC be successfully restarted?
- Can a destination write a batch of records?

An example of implemented acceptance tests can be found in
the [Kafka connector](https://github.com/ConduitIO/conduit-connector-kafka/blob/3233c37392e7249e9d64c9cae0374a0b47ca840f/acceptance_test.go).

### Debugging the connector

The steps for debugging a standalone connector have been
described [here](https://github.com/ConduitIO/conduit/discussions/1724#discussioncomment-10178484).
