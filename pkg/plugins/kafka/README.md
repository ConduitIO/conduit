### General
The Conduit Kafka plugin provides both, a source and a destination Kafka connector, for Conduit.

### How it works?
Under the hood, the plugin uses [Segment's Go Client for Apache Kafka(tm)](https://github.com/segmentio/kafka-go). It was 
chosen since it has no CGo dependency, making it possible to build the plugin for a wider range of platforms and architectures.
It also supports contexts, which will likely use in the future.

#### Source
A Kafka source connector is represented by a single consumer in a Kafka consumer group. By virtue of that, a source's 
logical position is the respective consumer's offset in Kafka. Internally, though, we're not saving the offset as the 
position: instead, we're saving the consumer group ID, since that's all which is needed for Kafka to find the offsets for
our consumer.

A source is getting associated with a consumer group ID the first time the `Read()` method is called.

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
|`acks`|destination|The number of acknowledgments required before considering a record written to Kafka. Valid values: 0, 1, all|false|`all`|
|`deliveryTimeout`|destination|Message delivery timeout.|false|`10s`|
|`readFromBeginning`|destination|Whether or not to read a topic from beginning (i.e. existing messages or only new messages).|false|`false`|

### Planned work
The planned work is tracked through [GitHub issues](https://github.com/ConduitIO/conduit/issues?q=is%3Aopen+label%3Aplugin%3Akafka).