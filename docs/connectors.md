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

For both, source and destination, the capabilities of the connector will be identified as:

* ✅ (GA) -
* ⚠️  (Beta) -

### The List

| Connector | Source | Destination | Support | Tested |
|-----------|-------|----|-------------|-----|
| Postgres   |✅ |✅ | Conduit |     |
| S3 |✅ |✅ | Conduit | |  
| Kafka |✅ |✅ | Conduit | | 
| File |✅ |✅ | Conduit | | 
| Random Generator |✅ | | Conduit | |
| Algolia | |✅ | Conduit | |
| Marketo |✅ | | Community | |
| Google Sheets |✅ | | Community | |
| Redis ||✅ | Community | |
| Zendesk |✅ |✅| Community | |
| Marketo |✅ | | Community | |
| Google Cloud Storage |✅ |✅ | Community | |
| BigQuery |✅ | | Community | |
| Stripe |✅ | | Community | |
| Snowflake |✅ | | Community | |
| Materialize | |✅ | Community | |
| GCP Pub/Sub |✅ | | Community | |
| Elastic Search |✅ |✅ | Community | |
| Azure Storage |✅ | | Community | |
| Nats | | | Community | |
| Nats Jetstream | | | Community | |
| Firebolt | | | Community | |
| Airtable | | | Community | |
| HTTP Server | | | Community | |
