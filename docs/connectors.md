# Conduit Connectors

Connectors are an integral part of Conduit. Conduit ships with a couple of connectors that are built into the service
to help developers bootstrap pipelines much more quickly. The built-in connectors include Postgres, File, Random Data
Generator, Kafka and Amazon S3 connectors.

The difference between Conduit connectors and those you might find from other services is that Conduit connectors are
Change Data Capture-first (CDC). CDC allows your pipeline to only get the changes that have happened over time instead
of pulling down an entire upstream data store and then tracking diffs between some period of time. This is critical for
building real-time event-driven pipelines and applications. But, we'll note where connectors don't have CDC capabilities.

## Roadmap & Feedback

If you need support for a particular data store that doesn't exist on the connector list, check out the list of
requested [source connectors](https://github.com/ConduitIO/conduit/issues?q=is%3Aissue+label%3Aconnector%3Asource+) and
the list of requested [destination connectors](https://github.com/ConduitIO/conduit/issues?q=is%3Aissue+label%3Aconnector%3Adestination).
Give the issue a `+1` if you really need that connector. The upvote will help the team understand demand for any
particular connector. If you find that an issue hasn't been created for your data store, please create a new issue in
the Conduit repo.

## Connectors

### Support Types

- `Conduit` - These are connectors that are built by the Conduit. Any issues or problems filed on those repos will be
  respond to by the Conduit team.
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
