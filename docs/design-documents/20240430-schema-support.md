<h1>Schema support</h1>

<!-- TOC -->
  * [The problem](#the-problem)
  * [Open questions](#open-questions)
  * [Requirements](#requirements)
  * [Support for schemas originating from other streaming tools](#support-for-schemas-originating-from-other-streaming-tools)
  * [Schema format](#schema-format)
  * [Implementation](#implementation)
    * [Schema storage and service implementation](#schema-storage-and-service-implementation)
      * [Option 1: Conduit itself hosts the schema registry](#option-1-conduit-itself-hosts-the-schema-registry)
      * [Option 2: A centralized, external schema registry, accessed through Conduit](#option-2-a-centralized-external-schema-registry-accessed-through-conduit)
      * [Option 3: A centralized, external schema registry, accessed by connectors directly](#option-3-a-centralized-external-schema-registry-accessed-by-connectors-directly)
      * [Chosen option](#chosen-option)
    * [Schema format](#schema-format-1)
      * [Schema service interface](#schema-service-interface)
        * [Option 1: Stream of commands and responses](#option-1-stream-of-commands-and-responses)
        * [Option 2: Exposing a gRPC service in Conduit](#option-2-exposing-a-grpc-service-in-conduit)
        * [Chosen option](#chosen-option-1)
  * [Summary](#summary)
  * [Other considerations](#other-considerations)
<!-- TOC -->

## The problem

A Conduit user would like to start using a destination connector **without**
manually setting up
the [target collections](https://conduit.io/docs/introduction/vocabulary).

To do so, the following is required:

- collection name
- collection metadata
- schema

This design document focuses on the schema support in Conduit in the context of
the above.

## Open questions

1. **Q**: Should a schema be accessible across Conduit instances?

   Schemas need to be accessible across pipelines (requirement #3). If the
   schema registry is part of Conduit, should we make it accessible via the API?

   **A**:
2. **Q**: Should Conduit allow schemas from other streaming tools?

   Conduit is sometimes used together with other streaming tools, such as Kafka
   Connect. For example, Kafka Connect is used to stream data to a topic, and
   then Conduit is used to read the data from the topic and write it into a
   destination.

   **A**:
3. **Q**: Should schemas be available in processors?

## Requirements

1. Records **should not** carry the full schema. 

   Reason: If a record would carry the whole schema, that might increase a
   record's size significantly.
2. Sources and destinations need to be able to work with multiple schemas.
   
   Reason: Multiple collections support.
3. A schema should be accessible across pipelines and Conduit instances.

   Reason: Pipelines might work together to stream data from A to B, through an
   intermediary C. For example, PostgreSQL to Kafka, and then Kafka to
   Snowflake. Also, in the future, pipelines might run in isolation on different
   instances.
4. It should be possible for a schema to evolve.
5. A source connector should be able to register a schema.
6. A destination connector should be able to fetch a specific schema.
7. A destination connector needs a way to know that a schema changed.
8. The Connector SDK should cache the schemas.

   Reason: Standalone connectors and Conduit communicate via gRPC. To avoid the
   cost of repeatedly fetching the same schema many times (especially over
   gRPC), schemas should be cached by the SDK.

## Support for schemas originating from other streaming tools

Conduit is sometimes used alongside other streaming tools. For example, Kafka
Connect may be used to read data from a source and write it into a topic.
Conduit then reads messages from that topic and writes it into a destination. We
also have the Kafka Connect wrapper which makes it possible to use Kafka Connect
connectors with Conduit. Here we have two possibilities:

1. The schema is part of the record (e.g. Debezium records)
2. The schema can be found in a schema registry (e.g. an Avro schema registry)

Following options are possible here:
1. schema is left as part of the record
2. schema is copied into the schema registry

TODO

## Schema format

A destination connector should work with one schema format only, regardless of
the underlying or intermediary schema formats used. This makes it easy for a
connector developer to write code, since it doesn't require handling of
potentially multiple schema types.

A schema consists of following:
- reference: a string that uniquely identifies a schema in Conduit
- list of fields, where each field is described with following:
    - name
    - type
    - optional (boolean value)
    - default value

The following types are supported:

- basic:
    - boolean
    - integers: 8, 16, 32, 64-bit
    - float: single precision (32-bit) and double precision (64-bit) IEEE 754 floating-point number
    - bytes
    - string
    - timestamp
- complex:
    - array
    - map
    - struct
    - union

Every field in a schema can be marked as optional (nullable).

## Implementation

### Schema storage and service implementation

#### Option 1: Conduit itself hosts the schema registry

A schema registry can be implemented within Conduit.

#### Option 2: A centralized, external schema registry, accessed through Conduit

A standalone schema service (such
as [Apicurio Registry](https://www.apicur.io/registry/)) can be used to manage
schemas. A single schema service deployment is accessed by multiple Conduit
instances. Connectors access the schema registry through Conduit.

#### Option 3: A centralized, external schema registry, accessed by connectors directly

This option is similar to the above, in the sense that a centralized schema
registry is used. However, in this option, Conduit is not used as an
intermediary. Rather, connectors access the schema registry directly. 

#### Chosen option

Option 1 keeps the tech stack simple. However, it also means that connectors
fetching schemas need to be aware which Conduit instance is managing them.

Options 2 and 3 remove that complexity at the expense of adding a new infrastructure
item.

Having Conduit as an intermediary makes schema registry updates easier, so
option 2 is the suggested option.

### Schema format

Conduit exposes a schema service that provides the required schema operations.
The implementation of the service is discussed in a later section.

Schema support is added as part of the OpenCDC standard. A schema is represented
by the following Protobuf message:

```protobuf
syntax = "proto3";

message Schema {
  string id = 1;
  repeated Field fields = 2;
}

message FieldType {
  oneof type {
    PrimitiveFieldType primitiveType = 1;
    ArrayType arrayType = 2;
    MapType mapType = 3;
    StructType structType = 4;
    UnionType unionType = 5;
  }
}

enum PrimitiveFieldType {
  BOOLEAN = 0;
  INT8 = 1;
  // other primitive types
}

message ArrayType {
  FieldType elementType = 1;
}

message MapType {
  FieldType keyType = 1;
  FieldType valueType = 2;
}

message StructType {
  repeated Field fields = 1;
}

message UnionType {
  repeated FieldType types = 1;
}

message Field {
  string name = 1;
  oneof type {
    PrimitiveFieldType primitiveType = 2;
    ArrayType arrayType = 3;
    MapType mapType = 4;
    StructType structType = 5;
    UnionType unionType = 6;
  }
  bool optional = 7;
  // todo: find appropriate type
  any defaultValue = 8;
}
```

The Connector SDK will provide Go types and functions to work with schemas, i.e.
a connector developer won't work with Protobuf directly.

#### Schema service interface

This section discusses the schema service's interface.

##### Option 1: Stream of commands and responses

This pattern is used in WASM processors. A server (in this case: Conduit)
listens to commands (in this case: via a bidirectional gRPC stream). A client (
in this case: a connector) sends a command to either register a schema or fetch
a schema. Conduit receives the command and replies. An example can be seen below:

```protobuf
rpc CommandStream(stream Command) returns (stream Response);
```

For different types of commands and response to be supported, `Command`
and `Response` need to have a `oneof` field where all the possible commands
and respective responses are listed:

```protobuf
message Command {
    oneof cmd {
        SaveSchemaCommand saveSchemaCmd = 1;
        // etc.
   }
}

message Response {
  oneof resp {
    SaveSchemaResponse saveSchemaResp = 1;
    // etc.
  }
}
```    
**Advantages**:

1. No additional connection setup. When Conduit starts a connector process, it
   establishes a connection. The same connection is used for all communication (
   e.g. configuring a connector, opening, reading/writing records, etc.)
2. Connector actions (which are planned for a future milestone) might use the
   same command-and-reply stream.

**Disadvantages**:

1. Separate flow needed to establish a connector to a remote Conduit instance (
   see [requirement](#requirements) #3).
2. A single method for all the operations makes both, the server and client
   implementation, more complex. In Conduit, a single gRPC method needs to check
   for the command type and then reply with a response. Then the client (i.e.
   the connector) needs to check the response type. In case multiple commands
   are sent, we need ordering guarantees.

##### Option 2: Exposing a gRPC service in Conduit

Conduit exposes a service to work with schemas. Connectors access the service
and call methods on the service. 

For this work, a connector (i.e. clients of the schema service) needs Conduit's
IP address and the gRPC port. The IP address can be fetched
using [peer](https://pkg.go.dev/google.golang.org/grpc/peer#Peer). Conduit can
send its gRPC port to the connector via the `Configure` method.

**Advantages**: 

1. Works with a remote Conduit instance (see [requirement](#requirements) #3).
2. Easy to understand: the gRPC methods, together with requests and responses,
   can easily be understood from a proto file.
3. An HTTP API for the schema registry can easily be exposed (if needed).

**Disadvatanges**:

1. Changes needed to communicate Conduit's gRPC port to the connector.

##### Chosen option

**Option 2** is the chosen method since it offers more clarity and the support
for remote Conduit instances.

## Summary

The following design is proposed: 

Conduit exposes a gRPC service for managing schemas. When Conduit starts a
connector, it sends the connector its gRPC port. A connector creates and fetches
schemas through the gRPC service.

Conduit's gRPC service is an abstraction/indirection for an external schema
registry (such as Apicurio Registry), that is accessed by multiple Conduit
instances.

## Other considerations

1. **Permissions**: creating a collection generally requires a broader set of
   permissions then just writing data to a collection. For some users, the
   benefit of having restricted permissions might outweigh the benefit of
   auto-creating collections.
