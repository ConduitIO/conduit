# Support for Multiple Collections

## Introduction

The aim of this feature is to enhance Conduit's capability to handle multiple
tables within its connectors effectively. This involves modifying existing
connectors and introducing new metadata fields to facilitate seamless
integration with various data sources and destinations.

### Goals

- Enable connectors to read from and write to multiple tables efficiently.
- Introduce standardized metadata fields to ensure interoperability and
  configurability.

## Implementation Overview

### Proto Constants

We will introduce a constant for a record metadata field `opencdc.collection`
used to indicate the source/destination collection of a record. This should be
added
to [`opencdc.proto`](https://github.com/ConduitIO/conduit-commons/blob/main/proto/opencdc/v1/opencdc.proto).

Sidenote: While changing proto files, we could take the opportunity to create a
new proto specifically for metadata constants (`metadata/constants.proto`). It
should include existing constants defined
in [`connector.proto`](https://github.com/ConduitIO/conduit-connector-protocol/blob/a276bef021ca005f51456a06820e5bddce3e9620/proto/connector/v1/connector.proto#L10-L13),
which would expose them not only to connectors, but also processors. We are also
missing constants populated in DLQ records (currently only defined
as [Go constants](https://github.com/ConduitIO/conduit-commons/blob/8791ac01c9949cb404bf8ff1ec5c25051ecbcfbc/opencdc/metadata.go#L59-L67)).

### Metadata Utility function

The introduced metadata field should get its own getter and setter on
the [`opencdc.Metadata`](https://github.com/ConduitIO/conduit-commons/blob/8791ac01c9949cb404bf8ff1ec5c25051ecbcfbc/opencdc/metadata.go#L70)
type, same as any other constant.

### Connector Support

#### Postgres Connector

- **Source:**
  - Enable reading from multiple tables, replacing the existing `postgres.table`
    field with `opencdc.collection`.
  - Implement wildcard functionality to listen to all tables (`*`).
- **Destination:**
  - Support routing records to a target table based on data taken from the
    record. This behavior should be configurable through the `table` parameter,
    which should default to `{{ .Metadata["opencdc.collection"] }}`.

#### Generator Connector

- **Source:**
  - Allow generating records that include the metadata
    field `opencdc.collection`.
  - Ideally the user should be able to specify different collections and
    associated field mappings.

#### Kafka Connector

- **Source:**
  - Attach the Kafka topic name to records as `opencdc.collection`, replacing
    the current `kafka.topic` key.
- **Destination:**
  - Support routing records to a target topic based on data taken from the
    record. This behavior should be configurable through the `topic` parameter,
    which should default to `{{ .Metadata["opencdc.collection"] }}`.
