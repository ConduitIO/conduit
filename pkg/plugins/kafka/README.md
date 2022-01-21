### General
The Conduit Kafka plugin provides both, a destination and source Kafka connector, for Conduit.

### How it works?
Under the hood, the plugin uses [Segment's Go Client for Apache Kafka(tm)](https://github.com/segmentio/kafka-go).
This client supports a wide range of configuration parameters, which makes it possible to fine tune the plugin.

#### Source
The Kafka source manages the offsets manually. The main reason for this is that the source connector needs to be able to
"seek" to any offset in a Kafka topic.

If a messages is not received from a broker in a specified timeout (which is 5 seconds, and defined by `msgTimeout` in `source.go`),
the Kafka source returns a "recoverable error", which indicates to Conduit that it should try reading data after some time again.

#### Destination
The destination connector uses **synchronous** writes to Kafka. Proper buffering support which will enable asynchronous 
(and more optimal) writes is planned. 

### How to build?
Run `make build-kafka-plugin`.

### Testing
Run `make test` to run all the unit tests. Run `make test-integration` to run the integration tests.

The integration tests assume that an instance of Kafka at `localhost:9092` is running.
The Docker compose file at `test/docker-compose.yml` can be used to quickly start a Kafka instance. 

### Configuration
There's no global, plugin configuration. Each connector instance is configured separately. 

| name | part of | description | required | default value |
|------|---------|-------------|----------|---------------|
|`servers`|destination, source|A list of bootstrap servers to which the plugin will connect.|true| |
|`topic`|destination, source|The topic to which records will be written to.|true| |
|`securityProtocol`|destination, source|Protocol used to communicate with brokers. Valid values are: PLAINTEXT, SSL, SASL_PLAINTEXT, SASL_SSL.|false| |
|`acks`|destination|The number of acknowledgments required before considering a record written to Kafka. Valid values: 0, 1, all|false|`all`|
|`deliveryTimeout`|destination|Message delivery timeout.|false|`10s`|
|`readFromBeginning`|destination|Whether or not to read a topic from beginning (i.e. existing messages or only new messages).|false|`false`|

### Planned work
The planned work is tracked through [GitHub issues](https://github.com/ConduitIO/conduit/issues?q=is%3Aopen+label%3Aplugin%3Akafka).