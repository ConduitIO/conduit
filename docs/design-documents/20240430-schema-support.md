# Schema support

## The problem

A Conduit user would like to start using a destination connector **without**
manually setting up
the [target collections](https://conduit.io/docs/introduction/vocabulary).

To do so, the following is required:

* collection name
* collection metadata
* schema

This design document focuses on the schema support in Conduit in the context of
the above.

## Requirements

1. Records **should not** carry the full schema. 

   Reason: If a record would carry the whole schema, that would increase the
   record size a lot.
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

## Schema format

A destination connector should work with one schema format only, regardless of
the underlying or intermediary schema formats used. This makes it easy for a
connector developer to write code, since it doesn't require handling of
potentially multiple schema types.

A schema consists of following:
* reference: a string that uniquely identifies a schema in Conduit
* list of fields, where each field is described with following:
  * name
  * type
  * optional (boolean value)
  * default value

The following types are supported:
* Primitive:
  * boolean
  * integers: 8, 16, 32, 64-bit 
  * float: single precision (32-bit) and double precision (64-bit) IEEE 754 floating-point number
  * bytes
  * string
* Complex:
  * array
  * map
  * struct
  * union

Every field in a schema can be marked as optional (nullable).

## Implementation

Schema support is part of the OpenCDC standard. A schema is represented by the
following Protobuf message:

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

A reference to a schema is saved in a new metadata field, `opencdc.schemaID`.

### Schema-related operations in connectors

## Questions

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

## Other considerations

1. **Permissions**: creating a collection generally requires a broader set of
   permissions then just writing data to a collection. For some users, the
   benefit of having restricted permissions might outweigh the benefit of
   auto-creating collections.