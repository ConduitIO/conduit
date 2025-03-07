# Conduit

![Logo](https://raw.githubusercontent.com/ConduitIO/.github/main/images/conduit-logo-outlined.svg)

_Data Integration for Production Data Stores. :dizzy:_

![scarf pixel](https://static.scarf.sh/a.png?x-pxid=ae2e6c1b-5c87-4c4f-80bd-dd598b2e3c3b)
[![License](https://img.shields.io/badge/license-Apache%202-blue)](https://github.com/ConduitIO/conduit/blob/main/LICENSE.md)
[![Test](https://github.com/ConduitIO/conduit/actions/workflows/test.yml/badge.svg)](https://github.com/ConduitIO/conduit/actions/workflows/test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/conduitio/conduit)](https://goreportcard.com/report/github.com/conduitio/conduit)
[![Discord](https://img.shields.io/discord/828680256877363200?label=discord&logo=discord)](https://discord.meroxa.com)
[![Go Reference](https://pkg.go.dev/badge/github.com/conduitio/conduit.svg)](https://pkg.go.dev/github.com/conduitio/conduit)
[![Conduit docs](https://img.shields.io/badge/conduit-docs-blue)](https://www.conduit.io/docs/introduction/getting-started)
[![API docs](https://img.shields.io/badge/HTTP_API-docs-blue)](https://docs.conduit.io/api)

## Overview

Conduit is a data streaming tool written in Go. It aims to provide the best user
experience for building and running real-time data pipelines. Conduit comes with
common connectors, processors and observability data out of the box.

Conduit pipelines are built out of simple building blocks which run in their own
goroutines and are connected using Go channels. This makes Conduit pipelines
incredibly performant on multi-core machines. Conduit guarantees the order of
received records won't change, it also takes care of consistency by propagating
acknowledgements to the start of the pipeline only when a record is successfully
processed on all destinations.

Conduit connectors are plugins that communicate with Conduit via a gRPC
interface. This means that plugins can be written in any language as long as
they conform to the required interface.

Conduit was created and open-sourced by [Meroxa](https://meroxa.io).

- [Quick start](#quick-start)
- [Installation guide](#installation-guide)
- [Configuring Conduit](#configuring-conduit)
- [Storage](#storage)
- [Connectors](#connectors)
- [Processors](#processors)
- [API](#api)
- [Documentation](#documentation)
- [Contributing](#contributing)

## Quick start

<https://conduit.io/docs/getting-started>

## Installation guide

### Download binary and run

Download a pre-built binary from
the [latest release](https://github.com/conduitio/conduit/releases/latest) and
simply run it!

```sh
./conduit run
```

Once you see that the service is running, the configured pipeline should start
processing records automatically. You can also interact with
the [Conduit API](#api) directly, we recommend navigating
to `http://localhost:8080/openapi` and exploring the HTTP API through Swagger
UI.

Conduit can be configured through command line parameters. To view the full list
of available options, run `./conduit run --help` or see
[configuring Conduit](#configuring-conduit).

### Homebrew

Make sure you have [homebrew](https://brew.sh/) installed on your machine, then run:

```sh
brew update
brew install conduit
```

### Debian

Download the right `.deb` file for your machine architecture from the
[latest release](https://github.com/conduitio/conduit/releases/latest), then run:

```sh
dpkg -i conduit_0.13.2_Linux_x86_64.deb
```

### RPM

Download the right `.rpm` file for your machine architecture from the
[latest release](https://github.com/conduitio/conduit/releases/latest), then run:

```sh
rpm -i conduit_0.13.2_Linux_x86_64.rpm
```

### Build from source

Requirements:

- [Go](https://golang.org/)
- [Make](https://www.gnu.org/software/make/)

```shell
git clone git@github.com:ConduitIO/conduit.git
cd conduit
make
./conduit run
```

### Docker

Our Docker images are hosted on GitHub's Container Registry. To run the latest
Conduit version, you should run the following command:

```sh
docker run -p 8080:8080 conduit.docker.scarf.sh/conduitio/conduit:latest
```

## Configuring Conduit

Conduit accepts CLI flags, environment variables and a configuration file to
configure its behavior. Each CLI flag has a corresponding environment variable
and a corresponding field in the configuration file. Conduit uses the value for
each configuration option based on the following priorities:

- **CLI flags (highest priority)** - if a CLI flag is provided it will always be
  respected, regardless of the environment variable or configuration file. To
  see a full list of available flags run `conduit run --help`.
- **Environment variables (lower priority)** - an environment variable is only
  used if no CLI flag is provided for the same option. Environment variables
  have the prefix `CONDUIT` and contain underscores instead of dots and
  hyphens (e.g. the flag `-db.postgres.connection-string` corresponds to
  `CONDUIT_DB_POSTGRES_CONNECTION_STRING`).
- **Configuration file (lowest priority)** - By default, Conduit loads a
  configuration file named `conduit.yaml` located in the same directory as the
  Conduit binary. You can customize the directory path to this file using the
  CLI flag `--config.path`. The configuration file is optional, as any value
  specified within it can be overridden by an environment variable or a CLI
  flag.

  The file must be a YAML document, and keys can be hierarchically structured
  using `.`. For example:

  ```yaml
  db:
    type: postgres # corresponds to flag -db.type and env variable CONDUIT_DB_TYPE
    postgres:
      connection-string: postgres://localhost:5432/conduitdb # -db.postgres.connection-string or CONDUIT_DB_POSTGRES_CONNECTION_STRING
  ```

  To generate a configuration file with default values, use:
  `conduit init --path <directory where conduit.yaml will be created>`.

This parsing configuration is provided thanks to our own CLI
library [ecdysis](https://github.com/conduitio/ecdysis), which builds on top
of [Cobra](https://github.com/spf13/cobra) and
uses [Viper](https://github.com/spf13/viper) under the hood.

## Storage

Conduit's own data (information about pipelines, connectors, etc.) can be stored
in the following databases:

- BadgerDB (default)
- PostgreSQL
- SQLite

It's also possible to store all the data in memory, which is sometimes useful
for development purposes.

The database type used can be configured with the `db.type` parameter (through
any of the [configuration](#configuring-conduit) options in Conduit).
For example, the CLI flag to use a PostgreSQL database with Conduit is as
follows: `-db.type=postgres`.

Changing database parameters (e.g. the PostgreSQL connection string) is done
through parameters of the following form: `db.<db type>.<parameter name>`. For
example, the CLI flag to use a PostgreSQL instance listening on `localhost:5432`
would be: `-db.postgres.connection-string=postgres://localhost:5432/conduitdb`.

The full example in our case would be:

```shell
./conduit run -db.type=postgres -db.postgres.connection-string="postgresql://localhost:5432/conduitdb"
```

## Connectors

For the full list of available connectors, see
the [Connector List](https://conduit.io/docs/using/connectors/list). If
there's a connector that you're looking for that isn't available in Conduit,
please file an [issue](https://github.com/ConduitIO/conduit/issues/new?assignees=&labels=triage&template=3-connector-request.yml&title=Connector%3A+%3Cresource%3E+%5BSource%2FDestination%5D)
.

Conduit loads standalone connectors at startup. The connector binaries need to
be placed in the `connectors` directory relative to the Conduit binary so
Conduit can find them. Alternatively, the path to the standalone connectors can
be adjusted using the CLI flag `-connectors.path`.

Conduit ships with a number of built-in connectors:

- [File connector](https://github.com/ConduitIO/conduit-connector-file) provides
  a source/destination to read/write a local file (useful for quickly trying out
  Conduit without additional setup).
- [Kafka connector](https://github.com/ConduitIO/conduit-connector-kafka)
  provides a source/destination for Apache Kafka.
- [Postgres connector](https://github.com/ConduitIO/conduit-connector-postgres)
  provides a source/destination for PostgreSQL.
- [S3 connector](https://github.com/ConduitIO/conduit-connector-s3) provides a
  source/destination for AWS S3.
- [Generator connector](https://github.com/ConduitIO/conduit-connector-generator)
  provides a source which generates random data (useful for testing).
- [Log connector](https://github.com/ConduitIO/conduit-connector-log)
  provides a destination which logs all records (useful for testing).
  
Additionally, we have prepared
a [Kafka Connect wrapper](https://github.com/conduitio/conduit-kafka-connect-wrapper)
that allows you to run any Apache Kafka Connect connector as part of a Conduit
pipeline.

If you are interested in writing a connector yourself, have a look at
our [Go Connector SDK](https://github.com/ConduitIO/conduit-connector-sdk).
Since standalone connectors communicate with Conduit through gRPC they can be
written in virtually any programming language, as long as the connector follows
the [Conduit Connector Protocol](https://github.com/ConduitIO/conduit-connector-protocol).

## Processors

A processor is a component that operates on a single record that flows through a
pipeline. It can either change the record (i.e. **transform** it) or **filter**
it out based on some criteria.

Conduit provides a number of built-in processors, which can be used to
manipulate fields, send requests to HTTP endpoints, and more,
check [built-in processors](https://conduit.io/docs/using/processors/builtin/)
for the list of built-in processors and documentations.

Conduit also provides the ability to write your
own [standalone processor](https://conduit.io/docs/developing/processors/building),
or you can use the built-in processor [`custom.javascript`](https://conduit.io/docs/using/processors/builtin/custom.javascript)
to write custom processors in JavaScript.

More detailed information as well as examples can be found in
the [Processors documentation](https://conduit.io/docs/using/processors/getting-started).

## API

Conduit exposes a gRPC API and an HTTP API.

The gRPC API is by default running on port 8084. You can define a custom address
using the CLI flag `-api.grpc.address`. To learn more about the gRPC API please have
a look at
the [protobuf file](https://github.com/ConduitIO/conduit/blob/main/proto/api/v1/api.proto).

The HTTP API is by default running on port 8080. You can define a custom address
using the CLI flag `-api.http.address`. It is generated
using [gRPC gateway](https://github.com/grpc-ecosystem/grpc-gateway) and is thus
providing the same functionality as the gRPC API. To learn more about the HTTP
API please have a look at the [API documentation](https://www.conduit.io/api),
[OpenAPI definition](https://github.com/ConduitIO/conduit/blob/main/pkg/web/openapi/swagger-ui/api/v1/api.swagger.json)
or run Conduit and navigate to `http://localhost:8080/openapi` to open
a [Swagger UI](https://github.com/swagger-api/swagger-ui) which makes it easy to
try it out.

## Documentation

To learn more about how to use Conduit
visit [Conduit.io/docs](https://conduit.io/docs).

If you are interested in internals of Conduit we have prepared some technical
documentation:

- [Pipeline Semantics](https://conduit.io/docs/core-concepts/pipeline-semantics) explains the internals of how
  a Conduit pipeline works.
- [Pipeline Configuration Files](https://conduit.io/docs/using/pipelines/configuration-file)
  explains how you can define pipelines using YAML files.
- [Processors](https://conduit.io/docs/using/processors/getting-started) contains examples and more information about
  Conduit processors.
- [Conduit Architecture](https://conduit.io/docs/core-concepts/architecture)
  will give you a high-level overview of Conduit.
- [Conduit Metrics](docs/metrics.md)
  provides more information about how Conduit exposes metrics.
- [Conduit Package structure](docs/package_structure.md)
  provides more information about the different packages in Conduit.

## Contributing

For a complete guide to contributing to Conduit, see
the [contribution guide](https://github.com/ConduitIO/conduit/blob/master/CONTRIBUTING.md).

We welcome you to join the community and contribute to Conduit to make it
better! When something does not work as intended please check if there is
already an [issue](https://github.com/ConduitIO/conduit/issues) that describes
your problem, otherwise
please [open an issue](https://github.com/ConduitIO/conduit/issues/new/choose)
and let us know. When you are not sure how to do something
please [open a discussion](https://github.com/ConduitIO/conduit/discussions) or
hit us up on [Discord](https://discord.meroxa.com).

We also value contributions in the form of pull requests. When opening a PR please
ensure:

- You have followed
  the [Code Guidelines](https://github.com/ConduitIO/conduit/blob/main/docs/code_guidelines.md).
- There is no other [pull request](https://github.com/ConduitIO/conduit/pulls)
  for the same update/change.
- You have written unit tests.
- You have made sure that the PR is of reasonable size and can be easily
  reviewed.
