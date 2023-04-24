# Schema Registry Support

## Introduction

This document describes the design for adding Schema Registry support to Conduit.

### Background

A Schema Registry is a tool that helps Conduit understand the format of the data
it receives. Often, data is sent in binary formats that can make it difficult to
interpret without knowing the schema beforehand. By adding support for a Schema
Registry, Conduit can understand how to parse the binary data and turn it into
structured data that can be manipulated by processors.

At the same time, uploading the schema of a structured payload to a registry can
also be helpful. This allows Conduit to take structured data and compress it
into a binary format for transmission. By doing so, Conduit can optimize the
amount of data being sent, making it faster and more efficient.

### Goals

This design should allow Conduit to do the following:

- Fetch a schema from a schema registry (e.g. Apicurio, Confluent schema registry).
- Use that schema to decode raw data into structured data (e.g. Avro, Protobuf).
- Encode structured data into raw data using a binary format + schema.
- Upload the schema to a schema registry.

**Note:** outputting a binary format in connectors is _not_ in the scope of this
design. Here we only target decoding and encoding the key and payload fields of
an OpenCDC record, but not the OpenCDC record itself.

## Implementation options

### Option 1 - 2 processors

Implement 2 separate processors.

The first processor knows how to fetch a schema from a schema registry and
decode raw data using the schema. It needs to be able to process fields
`Record.Key`, `Record.Paylod.Before` and/or `Record.Payload.After`. If the
schema is not found the processor fails.

The second processor is able to encode structured data into a binary format
(e.g. Avro, Protobuf) and produce a schema that can be used to decode it back to
structured data. It can also upload the schema to a schema registry. If the
schema can't be uploaded to the schema registry the processor fails.

By default, these processors use a predefined metadata field as the schema ID
(e.g. `conduit.key.schemaId`, `conduit.payload.before.schemaId` or
`conduit.payload.after.schemaId`). The user is able to override this behavior
and configure a custom schema ID using static data and/or data taken from the
record (e.g. metadata).

When other processors manipulate the record we do not need to track those
changes and update the schema, because the schema is extracted on demand by the
processor.

Each processor needs a schema registry URL in its configuration. To make this
simpler we can later add the option to configure a pipeline schema registry URL
and/or a global schema registry URL. The processor would then pick the first URL
it can find in the hierarchy (processor > pipeline > global).

#### Pros

- Connectors don't have to work with schemas.
- The `Record` type does not need to change.
- The record can be decoded/encoded at any point in the pipeline, the use can
  put the processor anywhere.
- We do not have to track changes done to a record by processors.

#### Cons

- User needs to understand and configure 2 processors.
- A schema can't be extracted from structured data in a lossless way.
  For example, consider an Avro schema where a field can contain multiple types
  and has a default value:

  ```json
  {
    "name": "example",
    "type": ["string", "int"],
    "default": "foo"
  },
  ```

  It's impossible to extract this schema from a concrete value without knowing
  the original schema.

### Option 2 - Attach Schema to Record

This option builds on top of option 1 and tries to address the last con,
lossless schema handling.

Conduit provides processors to decode and encode data using a schema, but
additionally attaches the schema to the record. This means Conduit needs to
somehow track changes done to the key and payload of a record and update the
schema accordingly. Every time a processor changes a field (i.e. creates,
updates, deletes it) Conduit needs to detect it and update the schema attached
to the record.

#### Pros

- The schema produced when encoding a record will closely match the schema used
  for decoding it. User expectations are met.

#### Cons

- Complicated implementation (NB: we have
  [some code](https://github.com/ConduitIO/conduit/tree/46e56a59aaf2d1e85e98522c9b613ff7fb8329fd/pkg/record/schema)
  that we could build upon).
- Potential loss of performance. Every time a processor returns a record we need
  to compare it to the previous record and look for changes to update the schema.
- Adding support for a new format will be harder, as we need to implement
  translations between that format and our internal schema.

### Questions

- There has to be an external schema registry running for a user to use this
  feature. Should we make it easier by including a schema registry in Conduit?
  - ?

### Recommendation

We propose to start with option 1 and add option 2 if/when we see that there's a
real need to produce the same schema as we get by the schema registry.
