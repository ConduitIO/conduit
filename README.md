# Conduit
![Logo](https://raw.githubusercontent.com/ConduitIO/.github/main/images/conduit-logo-outlined.svg)

_Build real-time data pipelines in minutes, not months :dizzy:_ **TODO change slogan**

[![License](https://img.shields.io/badge/license-Apache%202-blue)](https://github.com/ConduitIO/conduit/blob/main/LICENSE.md)
[![Build](https://github.com/ConduitIO/conduit/actions/workflows/build.yml/badge.svg)](https://github.com/ConduitIO/conduit/actions/workflows/build.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/ConduitIO/conduit.svg)](https://pkg.go.dev/github.com/ConduitIO/conduit)
[![Go Report Card](https://goreportcard.com/badge/github.com/conduitio/conduit)](https://goreportcard.com/report/github.com/conduitio/conduit)
[![Discord](https://img.shields.io/discord/828680256877363200?label=discord&logo=discord)](https://discord.meroxa.com)
[![Docs](https://img.shields.io/badge/conduit-docs-blue)](https://docs.conduit.io)

## Overview

Conduit is a data streaming tool written in Go. It aims to provide the best user experience for building and running
real-time data pipelines. Conduit comes with batteries included, it provides a UI, common connectors, transforms and
observability data out of the box.

Conduit pipelines are built out of simple building blocks which run in their own goroutines and are connected using Go
channels. This makes Conduit pipelines incredibly performant on multi-core machines. Conduit guarantees the order of
received records won't change, it also takes care of consistency by propagating acknowledgments to the start of the
pipeline only when a record is successfully processed on all destinations.

Conduit connectors are plugins that communicate with Conduit via a gRPC interface. This means that plugins can be
written in any language as long as they conform to the required interface. For more information see the
[Connector Plugins](https://github.com/ConduitIO/conduit/blob/main/docs/plugins.md) documentation.

Conduit was created and open-sourced by [Meroxa](https://meroxa.io).

- [Installation guide](#installation-guide)
- [Testing](#testing)
- [API](#api)
- [UI](#ui)
- [Documentation](#documentation)
- [Contributing](#contributing)

## Installation guide

### Download release

Download a pre-built binary from the [latest release](https://github.com/conduitio/conduit/releases/latest) and simply
run it!

```
./conduit
```

Once you see that the service is running you may access a user-friendly web interface at `http://localhost:8080/ui/`.
You can also interact with the [Conduit API](#api) directly, we recommend navigating to `http://localhost:8080/openapi/`
and exploring the HTTP API through Swagger UI.

### Build from source

Requirements:
* [Go](https://golang.org/) (1.17 or later)
* [Node.js](https://nodejs.org/) (14.x)
* [Yarn](https://yarnpkg.com/) (latest 1.x)
* [Ember CLI](https://ember-cli.com/)
* [Make](https://www.gnu.org/software/make/)

```shell
git clone git@github.com:ConduitIO/conduit.git
cd conduit
make build
./conduit
```

Note that you can also build Conduit with `make build-server`, which only compiles the server and skips the UI. This command
requires only Go and builds the binary much faster. That makes it useful for development purposes or for running Conduit
as a simple backend service.

### Docker

Our Docker images are hosted on GitHub's Container Registry. To pull the latest tag, you should run the following in your command line:
```
docker pull ghcr.io/conduitio/conduit:latest
```
The Docker images include the UI and the following plugins: S3, Postgres, Kafka, file and generator.


## Testing

Conduit tests are split in two categories: unit tests and integration tests. Unit tests can be run without any
additional setup while integration tests require additional services to be running (e.g. Kafka or Postgres).

Unit tests can be run with `make test`.

Integration tests require [Docker](https://www.docker.com/) to be installed and running, they can be run with
`make test-integration`. This command will handle starting and stopping docker containers for you.

## API

Conduit exposes a gRPC API and an HTTP API.

The gRPC API is by default running on port 8084. You can define a custom address using the CLI flag `-grpc.address`. To
learn more about the gRPC API please have a look at the
[protobuf file](https://github.com/ConduitIO/conduit/blob/main/proto/api/v1/api.proto).

The HTTP API is by default running on port 8080. You can define a custom address using the CLI flag `-http.address`. It
is generated using [gRPC gateway](https://github.com/grpc-ecosystem/grpc-gateway) and is thus providing the same
functionality as the gRPC API. To learn more about the HTTP API please have a look at the
[OpenAPI definition](https://github.com/ConduitIO/conduit/blob/main/pkg/web/openapi/swagger-ui/api/v1/api.swagger.json)
or run Conduit and navigate to `http://localhost:8080/openapi/` to open a
[Swagger UI](https://github.com/swagger-api/swagger-ui) which makes it easy to try it out.

## UI

Conduit comes with a web UI that makes building data pipelines a breeze, you can access it at
`http://localhost:8080/ui/`. See the [installation guide](#build-from-source) for instructions on how to build Conduit
with the UI.

For more information about the UI refer to the [Readme](ui/README.md) in `/ui`.

![animation](docs/data/animation.gif)

## Documentation

To learn more about how to use Conduit visit [docs.conduit.io](https://docs.conduit.io).

If you are interested in internals of Conduit we have prepared some technical documentation:
* [Conduit Architecture](https://github.com/ConduitIO/conduit/blob/main/docs/architecture.md) will give you a high-level
  overview of Conduit.
* [Conduit Metrics](https://github.com/ConduitIO/conduit/blob/main/docs/metrics.md) provides more information about how
  Conduit exposes metrics.
* [Connector Plugins](https://github.com/ConduitIO/conduit/blob/main/docs/plugins.md) contains insights about how 
  Conduit is communicating with connector plugins and how you can build your own connector plugin.

## Contributing

For a complete guide to contributing to Conduit, see the
[Contribution Guide](https://github.com/ConduitIO/conduit/blob/master/CONTRIBUTING.md).

We welcome you to join the community and contribute to Conduit to make it better! When something does not work as
intended please check if there is already an [issue](https://github.com/ConduitIO/conduit/issues) that describes your
problem, otherwise please [open an issue](https://github.com/ConduitIO/conduit/issues/new) and let us know. When you are
not sure how to do something please [open a discussion](https://github.com/ConduitIO/conduit/discussions) or hit us up
on [Discord](https://discord.meroxa.com).

We also value contributions in form of pull requests. When opening a PR please ensure:
- You have followed the [Code Guidelines](https://github.com/ConduitIO/conduit/blob/main/docs/code_guidelines.md).
- There is no other [pull request](https://github.com/ConduitIO/conduit/pulls) for the same update/change.
- You have written unit tests.
- You have made sure that the PR is of reasonable size and can be easily reviewed.
