# Conduit Connectors

Connectors are an integral part of Conduit. Conduit ships with a couple of connectors that are built into the service to help developers bootstrap pipelines much more quickly. The built-in connectors include Postgres, File, Kafka and Amazon S3 connectors.

The difference between Conduit connectors and those you might find from other services is that Conduit connectors are Change Data Capture-first (CDC). CDC allows your pipeline to only get the changes that have happened over time instead of pulling down an entire upstream data store and then tracking diffs between some period of time. This is critical for building real-time event-driven pipelines and applications. But, we'll note where connectors don't have CDC capabilities.


## Roadmap & Feedback

Based on what the Conduit team hears from developers, a [roadmap]() has been built to facilitate the connectors that need to be built for Conduit. If you have a connector that you really want to see supported or lend your vote for a particular connector, check out that roadmap and upvote the associated issue!

## Connectors

### Support Types

* `Conduit` - These are connectors that are built by the Conduit. Any issues or problems filed on those repos will be respond to by the Conduit team.
* `Community` - A community connector is one where a developer created a connector and they're the ones supporting it not the Conduit team.
* `Legacy` - Some connectors are built using non-preferred methods. For example, Kafka Connect connectors can be used on Conduit. This is considered a stop gap measure until `conduit` or `community` connectors are built.

At this time, Conduit does not have any commerically supported connectors.

### Source & Destination Stages

Source means, does the connector have the ability to get data from the upstream data store. Destination means, does the connector have the ability to write to the downstream data store.

### The List

| Connector | Source | Destination | Support |
|-----------|-------|----|-------------|
| [Algolia](https://github.com/ConduitIO/conduit-connector-algolia) | |✅ | Conduit |
| [Azure Storage](https://github.com/miquido/conduit-connector-azure-storage) |✅ | | Community |
| [BigQuery](https://github.com/neha-Gupta1/conduit-connector-bigquery) |✅ | | Community |
| [Elastic Search](https://github.com/miquido/conduit-connector-elasticsearch) |✅ |✅ | Community |
| [File](https://github.com/ConduitIO/conduit-connector-file) |✅ |✅ | Conduit |
| [Google Cloud Storage](https://github.com/WeirdMagician/conduit-connector-google-cloudstorage) |✅ |✅ | Community |
| [Google Sheets](https://github.com/gopherslab/conduit-connector-google-sheets) |✅ | | Community |
| [Kafka](https://github.com/ConduitIO/conduit-connector-kafka) |✅ |✅ | Conduit |
| [Marketo](https://github.com/rustiever/conduit-connector-marketo) |✅ | | Community |
| [Materialize](https://github.com/ConduitIO/conduit-connector-materialize) | |✅ | Community |
| [Postgres](https://github.com/ConduitIO/conduit-connector-postgres)   |✅ |✅ | Conduit |
| Random Generator |✅ | | Conduit |
| [Redis](https://github.com/gopherslab/conduit-connector-redis) ||✅ | Community |
| [S3](https://github.com/ConduitIO/conduit-connector-s3) |✅ |✅ | Conduit |
| [Salesforce](https://github.com/miquido/conduit-connector-salesforce) | ✅ | ✅ | Community |
| [Snowflake](https://github.com/ConduitIO/conduit-connector-snowflake) |✅ | | Community |
| [Stripe](https://github.com/ConduitIO/conduit-connector-stripe) |✅ | | Community |
| [Zendesk](https://github.com/gopherslab/conduit-connector-zendesk) |✅ |✅| Community |
