<h1>Schema support</h1>

<!-- TOC -->
  * [The goal](#the-goal)
  * [Requirements](#requirements)
  * [Schema structure](#schema-structure)
  * [Schema operations](#schema-operations)
    * [Create](#create)
    * [Fetch](#fetch)
  * [Implementation](#implementation)
    * [Schema storage](#schema-storage)
      * [Option 1: Conduit itself hosts the schema registry](#option-1-conduit-itself-hosts-the-schema-registry)
      * [Option 2: A centralized, external schema registry, accessed through Conduit](#option-2-a-centralized-external-schema-registry-accessed-through-conduit)
      * [Option 3: A centralized, external schema registry, accessed by connectors directly](#option-3-a-centralized-external-schema-registry-accessed-by-connectors-directly)
      * [Chosen option](#chosen-option)
    * [Schema format](#schema-format)
      * [Internal schema format](#internal-schema-format)
        * [Option 1: Avro](#option-1-avro)
        * [Option 2: Protobuf schema](#option-2-protobuf-schema)
      * [Chosen option](#chosen-option-1)
    * [Schema service interface](#schema-service-interface)
      * [Option 1: Stream of commands and responses](#option-1-stream-of-commands-and-responses)
      * [Option 2: Exposing a gRPC service in Conduit](#option-2-exposing-a-grpc-service-in-conduit)
      * [Chosen option](#chosen-option-2)
  * [Required changes](#required-changes)
    * [Conduit](#conduit)
    * [Connector SDK](#connector-sdk)
    * [Processor SDK](#processor-sdk)
  * [How are requirements addressed](#how-are-requirements-addressed)
  * [Summary](#summary)
  * [Other considerations](#other-considerations)
<!-- TOC -->

## The goal

The goal of schema support is to make the information about data types available
in a pipeline. This is needed to cover a few use cases, such as automatically
creating a destination collection or propagating schema changes.

## Requirements

1. Records **should not** carry the whole schema.

   Reason: If a record would carry the whole schema, that might increase a
   record's size significantly.
2. Sources and destinations need to be able to work with multiple schemas.
   
   Reason: Multiple collections support.
3. A schema should be accessible across pipelines and Conduit instances.

   Reason: Pipelines might work together to stream data from A to B, through an
   intermediary C. For example, PostgreSQL to Kafka, and then Kafka to
   Snowflake. Also, in the future, pipelines might run in isolation on different
   instances. Additionally, Conduit may be used so that there's one pipeline per
   instance. In such cases, we'd need this feature.
4. It should be possible for a schema to evolve.
5. A source connector should be able to register a schema.
6. A destination connector should be able to fetch a specific schema.
7. A destination connector needs a way to know that a schema changed.
8. The Connector SDK should provide an API to work with the schemas. 
9. The Connector SDK should cache the schemas.

   Reason: Standalone connectors and Conduit communicate via gRPC. To avoid the
   cost of repeatedly fetching or creating the same schema many times (
   especially over gRPC), schemas should be cached by the SDK.
10. Schema auto-generation

    Reason: Schema auto-generation is a useful feature when working with data
    sources that have structured data encoded into a binary format (e.g. a JSON
    file). In such cases, it's useful to have the schema auto-generated as a
    starting point for further processing and/or writing into a destination.

## Schema structure

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

Every field in a schema can be marked as optional (nullable). Alternatively,
nullable fields can also be represented as a union of the `null` type and the
field type. However, simply checking a boolean flag that a field is
optional/nullable is better developer experience.

## Schema operations

### Create

**Request**: A request to create a schema contains the schema name and at least
one field.

**Response**: If successful, the new schema's ID is returned. If a schema with
the same name and field specs exists, no new schema is created, and the existing
schema's ID is returned.

### Fetch

The only type of fetch supported is fetch by ID.

## Implementation

### Schema storage

#### Option 1: Conduit itself hosts the schema registry

The schema registry is implemented as part of Conduit. The schemas are stored in
Conduit's database.

**Advantages**

1. The tech stack is kept simple. One of the primary goals of Conduit is ease of
   deployment.
2. The schema operations that need to be implemented are relatively simple.

**Disadvantages**:

1. Implementing replication, fail-over, etc. to make the schema service
   production ready will require some time.

#### Option 2: A centralized, external schema registry, accessed through Conduit

A standalone schema service (such
as [Apicurio Registry](https://www.apicur.io/registry/)) is used to manage
the schemas. A single schema service deployment is accessed by multiple Conduit
instances. **Connectors access the schema registry through Conduit**.

**Advantages**:

1. Shortens the time to implement schema support in Conduit.
2. The schema service can be more easily changed.

**Disadvantages**:

1. Makes the tech stack more complex.

#### Option 3: A centralized, external schema registry, accessed by connectors directly

This option is similar to the above, in the sense that a centralized schema
registry is used. However, in this option, Conduit is not used as an
intermediary. Rather, **connectors access the schema registry directly**. 

**Advantages**:

1. Shortens the time to implement schema support in Conduit.

**Disadvantages**:

1. Makes the tech stack more complex.
2. Connectors need to follow the schema service's upgrades more closely.

#### Chosen option

Option 1 keeps the tech stack simple and is in line with Conduit's philosophy of
being deployed as a single binary. However, without replication, fail-over, etc.
it cannot be considered production ready.

Options 2 and 3 remove that complexity at the expense of adding a new
infrastructure item.

Having Conduit as an intermediary makes schema registry updates easier, so
option 2 is the suggested option.

However, **our goal is to eventually implement option 1**.

### Schema format

This section discusses the schema format to be used.

There are two aspects of this:

- the schema format used internally (when registering a schema in the schema
  service, updating it, fetching, etc.)
- the schema format exposed to connector developers (through the Connector SDK)

Having them different makes only sense if we expose our own format in the
Connector SDK. The internal format is dictated by the schema registry that we
will use, which supports only widely known formats, i.e. we won't be able to use
our own schema format for that purpose.

**Advantages**:
1. We can decouple the Connector SDK and Conduit release cycle from the schema
   internal format release cycle
2. We want to limit or add feature on top of internal the schema format
3. We can more easily switch the internal schema format, if needed

**Disadvantages**:

1. Newer features and fixes in the schema format used internally (e.g. Avro)
   sometimes need to be explicitly added to the schema format used
2. Boilerplate code that converts the SDK schema into the internal schema
3. Boilerplate code that exposes schema-related operations (encode/decode record payloads)

#### Internal schema format

##### Option 1: Avro

We use Avro as the schema format used by the Connector SDK and internally.

**Advantages**:

1. Schema is a first-class citizen
2. A widely used schema format.
3. A popular option with Kafka Connect (makes it easier for users to migrate)

**Disadvantages**:

##### Option 2: Protobuf schema

**Advantages**

1. Faster (de)serialization

**Disadvantages**:

1. Protobuf libs don't offer a way to create a `.proto` file, i.e. that needs to
   be done manually.

#### Chosen option

The schema registry that we plan to use (Apicurio Registry) is not constrained
to a single schema. Similar is true for Conduit as well.

Hence, we can make it possible for multiple schema formats to be used. The first
one to be supported is Avro. The Connector SDK will provide utility functions to
make building schemas easier.

### Schema service interface

This section discusses the schema service's interface. Below we discuss options
for the communication between Conduit and the connectors.

#### Option 1: Stream of commands and responses

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

1. A single method for all the operations makes both, the server and client
   implementation, more complex. In Conduit, a single gRPC method needs to check
   for the command type and then reply with a response. Then the client (i.e.
   the connector) needs to check the response type. In case multiple commands
   are sent, we need ordering guarantees.

#### Option 2: Exposing a gRPC service in Conduit

Conduit exposes a service to work with schemas. Connectors access the service
and call methods on the service. 

For this work, a connector (i.e. clients of the schema service) needs Conduit's
IP address and the gRPC port. The IP address can be fetched
using [peer](https://pkg.go.dev/google.golang.org/grpc/peer#Peer). Conduit can
send its gRPC port to the connector via the `Configure` method.

A skeleton of the gRPC definition of the service would be:

```protobuf
syntax = "proto3";

service SchemaService {
  rpc Create(CreateSchemaRequest) returns (CreateSchemaResponse);
  rpc Fetch(FetchSchemaRequest) returns (FetchSchemaResponse);
}

message CreateSchemaRequest {
  string name = 1;
  // other fields
}

message CreateSchemaResponse {
  string id = 1;
}

message FetchSchemaRequest {
  string id = 1;
}
message FetchSchemaResponse {}
```

**Advantages**: 

1. Easy to understand: the gRPC methods, together with requests and responses,
   can easily be understood from a proto file.
2. An HTTP API for the schema registry can easily be exposed (if needed).

**Disadvantages**:

1. Changes needed to communicate Conduit's gRPC port to the connector.

#### Chosen option

**Option 2** is the chosen method since it offers more clarity and the support
for remote Conduit instances.

## Required changes

### Conduit

Conduit needs to expose a gRPC service as explained above. The gRPC service
exposes methods needed for connectors to work with schemas.

When starting or configuring a connector, Conduit needs to send it its gRPC
port.

Internally, Conduit will use the Apicurio Registry to work with schemas. In future,
we plan to migrate to our own schema registry so that the tech stack is kept
simple and Conduit can be run with a single binary.

### Connector SDK

For the needs of source connectors, the Connector SDK needs to provide the
following functions:

1. A function/builder that builds an Avro schema.
2. A function that registers a schema.
3. A function that encodes a value using the built schema.

### Processor SDK

## How are requirements addressed

1. Records **should not** carry the whole schema.

   Addressed by saving the schemas in a schema service and records keeping a
   reference to the schema.
2. Sources and destinations need to be able to work with multiple schemas.

   The schema service doesn't put any limitations on source collections.
3. A schema should be accessible across pipelines and Conduit instances.

   Addressed through the usage of an external schema registry.
4. It should be possible for a schema to evolve.

   There's no explicit support for schema versions, as they're not needed. A new
   schema version can be created as a new schema object.
5. A source connector should be able to register a schema.

   The Connector SDK will provide a function to save a schema.
6. A destination connector should be able to fetch a specific schema.

   The Connector SDK will provide a function to fetch a schema.
7. A destination connector needs a way to know that a schema changed.

   A destination connector can compare the IDs or the records it received.
8. The Connector SDK should provide an API to work with the schemas.
9. The Connector SDK should cache the schemas.

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
2. **OpenCDC structured data**: it's becoming less useful, because it's types
   are limited. With the schema support, OpenCDC's `RawData` can be used
   everywhere.