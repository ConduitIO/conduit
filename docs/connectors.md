# Conduit Connectors

Connectors are an integral part of Conduit. Conduit ships with a couple of
[connectors that are directly built into](#built-in-connectors) the service,
but it can also be expanded with additional
[standalone connectors](#standalone-connectors).

One of the main differences between Conduit connectors and those that you might find from other services is
that all Conduit connectors are Change Data Capture-first (CDC).

CDC allows your pipeline to only get the changes that have happened over time
instead of pulling down an entire upstream data store and then tracking diffs
between some period of time.
This is critical for building real-time, event-driven pipelines and applications.
But, we'll note where connectors do or do not have CDC capabilities.

## Roadmap & Feedback

If you need support for a particular data store that doesn't exist on the connector list, check out the list of
requested [source connectors](https://github.com/ConduitIO/conduit/issues?q=is%3Aissue+label%3Aconnector%3Asource+is%3Aopen)
and the list of requested [destination connectors](https://github.com/ConduitIO/conduit/issues?q=is%3Aissue+label%3Aconnector%3Adestination+is%3Aopen).
Give the issue a `+1` if you really need that connector. The upvote will help the team understand demand for any
particular connector. If you find that an issue hasn't been created for your data store, please create a new issue in
the Conduit repo.

## More about Connectors

### Built-in connectors

To help developers bootstrap pipelines much more quickly,
Conduit ships with several built-in connectors by default.
This includes the Postgres, File, Random Data Generator, Kafka and Amazon S3 connectors.

If you are creating and distributing customized, pre-built Conduit binaries yourself,
you may want to modify the list of built-in connectors, too.
By modifying the list of built-in connectors,
your compiled binary will include a set of pre-installed connectors specific to your end users' needs,
and it can be run by others, as is, without them having to follow any additional installation instructions.

If you want to create your own Conduit binary with a different set of built-in connectors,
you can build-in an [existing connector](#the-list).
Alternatively, you can also create your own
[using the Conduit connector template or the Conduit Connector SDK](https://conduit.io/docs/connectors/building/).

Once you have chosen a connector to be built-in, you can:

- Import the Go module defining the connector
into the [builtin registry](https://github.com/ConduitIO/conduit/blob/main/pkg/plugin/builtin/registry.go)
and add a new key to `DefaultDispenserFactories `:

```go
package builtin

import (
  // ...
  file "github.com/conduitio/conduit-connector-file"
  // ...
  myNewConnector "github.com/foo/conduit-connector-new"
)

var (
  // DefaultDispenserFactories contains default dispenser factories for
  // built-in plugins. The key of the map is the import path of the module
  // containing the connector implementation.
  DefaultDispenserFactories = map[string]DispenserFactory{
    "github.com/conduitio/conduit-connector-file":    sdkDispenserFactory(file.Connector),
    // ...
    "github.com/foo/conduit-connector-new":           sdkDispenserFactory(myNewConnector.Connector),
  }
)
```

- Download the new package and its dependencies: `go get "github.com/foo/conduit-connector-new"`
- Run `make`
- You now have a binary with your own, custom set of built-in connectors! 🎉

### Standalone connectors

In addition to built-in connectors, Conduit can be used together with standalone connectors
to enable even more data streaming use cases.
The Conduit team and other community developers create and maintain standalone connectors.

Learn more about how you can install [standalone connectors to Conduit here](https://conduit.io/docs/connectors/installing).

### Support Types

- `Conduit` - These are connectors that are built by the Conduit. Any issues or problems filed on those repos will be
  responded to by the Conduit team.
- `Community` - A community connector is one where a developer created a connector and they're the ones supporting it
  not the Conduit team.
- `Legacy` - Some connectors are built using non-preferred methods. For example, Kafka Connect connectors can be used
  on Conduit. This is considered a stop gap measure until `conduit` or `community` connectors are built.

At this time, Conduit does not have any commercially supported connectors.

### Source & Destination

Source means the connector has the ability to get data from an upstream data store. Destination means the connector can
to write to a downstream data store.

### The List

| Connector | Source | Destination | Support | Conduit version support |
|-----------|-------|----|-------------|-------------|
| [Airtable](https://github.com/conduitio-labs/conduit-connector-airtable) |WIP| | Community | WIP |
| [Algolia](https://github.com/conduitio-labs/conduit-connector-algolia) | |✅ | Conduit | v0.3.0 |
| [Azure Storage](https://github.com/conduitio-labs/conduit-connector-azure-storage) |✅ | | Community | v0.3.0|
| [BigQuery](https://github.com/conduitio-labs/conduit-connector-bigquery) |✅ | | Community | v0.3.0|
| [DB2](https://github.com/conduitio-labs/conduit-connector-db2) |WIP|WIP| Community |WIP|
| [Elasticsearch](https://github.com/conduitio-labs/conduit-connector-elasticsearch) |✅ |✅ | Community |v0.3.0|
| [File](https://github.com/ConduitIO/conduit-connector-file) |✅ |✅ | Conduit |v0.3.0|
| [Firebolt](https://github.com/conduitio-labs/conduit-connector-firebolt) |WIP |WIP| Community | WIP |
| [GCP PubSub](https://github.com/conduitio-labs/conduit-connector-gcp-pubsub) |✅ |✅| Community |WIP |
| [Google Cloud Storage](https://github.com/conduitio-labs/conduit-connector-google-cloudstorage) |✅ | | Community | v0.3.0|
| [Google Sheets](https://github.com/conduitio-labs/conduit-connector-google-sheets) |✅ | | Community | v0.3.0|
| [Kafka](https://github.com/ConduitIO/conduit-connector-kafka) |✅ |✅ | Conduit |v0.3.0|
| [Kafka Connect Wrapper](https://github.com/ConduitIO/conduit-kafka-connect-wrapper) | ✅ | ✅ | Legacy |v0.3.0|
| [Marketo](https://github.com/conduitio-labs/conduit-connector-marketo) |✅ | | Community |v0.3.0|
| [Materialize](https://github.com/conduitio-labs/conduit-connector-materialize) | |✅ | Community |v0.3.0|
| [Nats Jetstream](https://github.com/conduitio-labs/conduit-connector-nats-jetstream) |✅ |✅ | Community |v0.3.0|
| [Nats PubSub](https://github.com/conduitio-labs/conduit-connector-nats-pubsub) |✅|✅ | Community |v0.3.0|
| [Oracle DB](https://github.com/conduitio-labs/conduit-connector-oracle) |WIP|WIP| Community |WIP|
| [Postgres](https://github.com/ConduitIO/conduit-connector-postgres)   |✅ |✅ | Conduit |v0.3.0|
| [Random Generator](https://github.com/ConduitIO/conduit-connector-generator) |✅ | | Conduit |v0.3.0|
| [Redis](https://github.com/conduitio-labs/conduit-connector-redis) |✅|✅ | Community |v0.3.0|
| [S3](https://github.com/ConduitIO/conduit-connector-s3) |✅ |✅ | Conduit | v0.3.0|
| [Salesforce](https://github.com/conduitio-labs/conduit-connector-salesforce) | ✅ | | Community | v0.3.0|
| [Snowflake](https://github.com/conduitio-labs/conduit-connector-snowflake) |✅ | | Community |v0.3.0|
| [Stripe](https://github.com/conduitio-labs/conduit-connector-stripe) |✅ | | Community |v0.3.0|
| [Vitess](https://github.com/conduitio-labs/conduit-connector-vitess) |WIP|WIP| Community |WIP|
| [Zendesk](https://github.com/conduitio-labs/conduit-connector-zendesk) |✅ |✅| Community |v0.3.0|
