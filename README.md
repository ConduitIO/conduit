# Conduit

![Logo](https://raw.githubusercontent.com/ConduitIO/.github/main/images/conduit-logo-outlined.svg)

_Data Integration for Production Data Stores. :dizzy:_

[![License](https://img.shields.io/badge/license-Apache%202-blue)](https://github.com/ConduitIO/conduit/blob/main/LICENSE.md)
[![Build](https://github.com/ConduitIO/conduit/actions/workflows/build.yml/badge.svg)](https://github.com/ConduitIO/conduit/actions/workflows/build.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/conduitio/conduit)](https://goreportcard.com/report/github.com/conduitio/conduit)
[![Discord](https://img.shields.io/discord/828680256877363200?label=discord&logo=discord)](https://discord.meroxa.com)
[![Go Reference](https://pkg.go.dev/badge/github.com/conduitio/conduit.svg)](https://pkg.go.dev/github.com/conduitio/conduit)
[![Conduit docs](https://img.shields.io/badge/conduit-docs-blue)](https://www.conduit.io/docs/introduction/getting-started)
[![API docs](https://img.shields.io/badge/HTTP_API-docs-blue)](https://docs.conduit.io/api)

## Overview

Conduit is a data streaming tool written in Go. It aims to provide the best user
experience for building and running real-time data pipelines. Conduit comes with
batteries included, it provides a UI, common connectors, processors and
observability data out of the box.

Conduit pipelines are built out of simple building blocks which run in their own
goroutines and are connected using Go channels. This makes Conduit pipelines
incredibly performant on multi-core machines. Conduit guarantees the order of
received records won't change, it also takes care of consistency by propagating
acknowledgments to the start of the pipeline only when a record is successfully
processed on all destinations.

Conduit connectors are plugins that communicate with Conduit via a gRPC
interface. This means that plugins can be written in any language as long as
they conform to the required interface.

Conduit was created and open-sourced by [Meroxa](https://meroxa.io).

- [Quick start](#quick-start)
- [Installation guide](#installation-guide)
- [Connectors](#connectors)
- [Processors](#processors)
- [API](#api)
- [UI](#ui)
- [Documentation](#documentation)
- [Contributing](#contributing)

## Quick start

1. Download and extract
   the [latest release](https://github.com/conduitio/conduit/releases/latest).
2. Download
   the [example pipeline](/examples/pipelines/file-to-file.yml)
   and put it in the directory named `pipelines` in the same directory as the
   Conduit binary.
3. Run Conduit (`./conduit`). The example pipeline will start automatically.
4. Write something to file `example.in` in the same directory as the Conduit
   binary.

   ```sh
   echo "hello conduit" >> example.in`
   ```

5. Read the contents of `example.out` and notice an OpenCDC record:

   ```sh
   $ cat example.out
   {"position":"MTQ=","operation":"create","metadata":{"file.path":"./example.in","opencdc.readAt":"1663858188836816000","opencdc.version":"v1"},"key":"MQ==","payload":{"before":null,"after":"aGVsbG8gY29uZHVpdA=="}}
   ```

6. The string `hello conduit` is a base64 encoded string stored in the field
   `payload.after`, let's decode it:

   ```sh
   $ cat example.out | jq ".payload.after | @base64d"
   "hello conduit"
   ```

7. Explore the UI by opening `http://localhost:8080` and build your own
   pipeline!

## Installation guide

### Download and run release

Download a pre-built binary from
the [latest release](https://github.com/conduitio/conduit/releases/latest) and
simply run it!

```sh
./conduit
```

Once you see that the service is running you may access a user-friendly web
interface at `http://localhost:8080`. You can also interact with
the [Conduit API](#api) directly, we recommend navigating
to `http://localhost:8080/openapi` and exploring the HTTP API through Swagger
UI.

Conduit can be configured through command line parameters. To view the full list
of available options, run `./conduit --help`.

### Build from source

Requirements:

- [Go](https://golang.org/) (1.20 or later)
- [Node.js](https://nodejs.org/) (16.x)
- [Yarn](https://yarnpkg.com/) (latest 1.x)
- [Ember CLI](https://ember-cli.com/)
- [Make](https://www.gnu.org/software/make/)

```shell
git clone git@github.com:ConduitIO/conduit.git
cd conduit
make
./conduit
```

Note that you can also build Conduit with `make build-server`, which only
compiles the server and skips the UI. This command requires only Go and builds
the binary much faster. That makes it useful for development purposes or for
running Conduit as a simple backend service.

### Docker

Our Docker images are hosted on GitHub's Container Registry. To run the latest
Conduit version, you should run the following command:

```sh
docker run -p 8080:8080 ghcr.io/conduitio/conduit:latest
```

The Docker image includes the [UI](#ui), you can access it by navigating
to `http://localhost:8080`.

## Connectors

For the full list of available connectors, see
the [Connector List](docs/connectors.md). If there's a connector that you're
looking for that isn't available in Conduit, please file
an [issue](https://github.com/ConduitIO/conduit/issues/new?assignees=&labels=triage&template=3-connector-request.yml&title=Connector%3A+%3Cresource%3E+%5BSource%2FDestination%5D)
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

Additionally, we have prepared
a [Kafka Connect wrapper](https://github.com/conduitio/conduit-kafka-connect-wrapper)
that allows you to run any Apache Kafka Connect connector as part of a Conduit
pipeline.

If you are interested in writing a connector yourself, have a look at our
[Go Connector SDK](https://github.com/ConduitIO/conduit-connector-sdk). Since
standalone connectors communicate with Conduit through gRPC they can be written
in virtually any programming language, as long as the connector follows
the [Conduit Connector Protocol](https://github.com/ConduitIO/conduit-connector-protocol)
.

## Processors

A processor is a component that operates on a single record that flows through a
pipeline. It can either change the record (i.e. **transform** it) or **filter**
it out based on some criteria.

Conduit provides a number of built-in processors, which can be used to filter
and replace fields, post payloads to HTTP endpoints etc. Conduit also provides
the ability to write custom processors in JavaScript.

More detailed information as well as examples can be found in
the [Processors documentation](/docs/processors.md).

## API

Conduit exposes a gRPC API and an HTTP API.

The gRPC API is by default running on port 8084. You can define a custom address
using the CLI flag `-grpc.address`. To learn more about the gRPC API please have
a look at
the [protobuf file](https://github.com/ConduitIO/conduit/blob/main/proto/api/v1/api.proto)
.

The HTTP API is by default running on port 8080. You can define a custom address
using the CLI flag `-http.address`. It is generated
using [gRPC gateway](https://github.com/grpc-ecosystem/grpc-gateway) and is thus
providing the same functionality as the gRPC API. To learn more about the HTTP
API please have a look at the [API documentation](https://www.conduit.io/api),
[OpenAPI definition](https://github.com/ConduitIO/conduit/blob/main/pkg/web/openapi/swagger-ui/api/v1/api.swagger.json)
or run Conduit and navigate to `http://localhost:8080/openapi` to open
a [Swagger UI](https://github.com/swagger-api/swagger-ui) which makes it easy to
try it out.

## UI

Conduit comes with a web UI that makes building data pipelines a breeze, you can
access it at `http://localhost:8080`. See
the [installation guide](#build-from-source) for instructions on how to build
Conduit with the UI.

For more information about the UI refer to the [Readme](ui/README.md) in `/ui`.

![animation](docs/data/animation.gif)

## Documentation

To learn more about how to use Conduit
visit [docs.Conduit.io](https://docs.conduit.io).

If you are interested in internals of Conduit we have prepared some technical
documentation:

- [Pipeline Semantics](docs/pipeline_semantics.md) explains the internals of how
  a Conduit pipeline works.
- [Pipeline Configuration Files](docs/pipeline_configuration_files.md)
  explains how you can define pipelines using YAML files.
- [Processors](docs/processors.md) contains examples and more information about
  Conduit processors.
- [Conduit Architecture](docs/architecture.md)
  will give you a high-level overview of Conduit.
- [Conduit Metrics](docs/metrics.md)
  provides more information about how Conduit exposes metrics.

## Contributing

For a complete guide to contributing to Conduit, see
the [Contribution Guide](https://github.com/ConduitIO/conduit/blob/master/CONTRIBUTING.md)
.

We welcome you to join the community and contribute to Conduit to make it
better! When something does not work as intended please check if there is
already an [issue](https://github.com/ConduitIO/conduit/issues) that describes
your problem, otherwise
please [open an issue](https://github.com/ConduitIO/conduit/issues/new/choose)
and let us know. When you are not sure how to do something
please [open a discussion](https://github.com/ConduitIO/conduit/discussions) or
hit us up on [Discord](https://discord.meroxa.com).

We also value contributions in form of pull requests. When opening a PR please
ensure:

- You have followed
  the [Code Guidelines](https://github.com/ConduitIO/conduit/blob/main/docs/code_guidelines.md)
  .
- There is no other [pull request](https://github.com/ConduitIO/conduit/pulls)
  for the same update/change.
- You have written unit tests.
- You have made sure that the PR is of reasonable size and can be easily
  reviewed.
