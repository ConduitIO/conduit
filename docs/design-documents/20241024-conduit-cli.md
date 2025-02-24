# Conduit CLI

## Introduction

The goal of this design document is to clarify which part of this
[Conduit discussion](https://github.com/ConduitIO/conduit/discussions/1642) will be part of the
upcoming [0.13 release](https://github.com/ConduitIO/conduit/milestone/15).

The intention is to provide a good starting experience and evolve it over time with more complex and advanced features.

### Goals

We aim to provide our users with an interface that empowers them to use the **main** Conduit features effortlessly.
While we have envisioned some complex features, they will be available in future releases.

We will maintain the current installation methods by incorporating the CLI through the same established processes.

The CLI needs to feel intuitive and align with our standards.

## Background

Here's how the existing Conduit CLI looks like:

```shell
$ conduit --help
Usage of conduit:
  -api.enabled
      enable HTTP and gRPC API (default true)
  -config string
      global config file (default "conduit.yaml")
  -connectors.path string
      path to standalone connectors' directory (default "./connectors")
  -db.badger.path string
      path to badger DB (default "conduit.db")
  -db.postgres.connection-string string
      postgres connection string
  -db.postgres.table string
      postgres table in which to store data (will be created if it does not exist) (default "conduit_kv_store")
  -db.type string
      database type; accepts badger,postgres,inmemory (default "badger")
  -grpc.address string
      address for serving the gRPC API (default ":8084")
  -http.address string
      address for serving the HTTP API (default ":8080")
  -log.format string
      sets the format of the logging; accepts json, cli (default "cli")
  -log.level string
      sets logging level; accepts debug, info, warn, error, trace (default "info")
  -pipelines.exit-on-error
      exit Conduit if a pipeline experiences an error while running
  -pipelines.path string
      path to the directory that has the yaml pipeline configuration files, or a single pipeline configuration file (default "./pipelines")
  -processors.path string
      path to standalone processors' directory (default "./processors")
  -version
      prints current Conduit version
```

## Suggested commands for Conduit 0.13

The following list contains the suggested commands we propose to include in the first iteration.

New and more complex commands will be added later.

### `conduit init`

This command will initialize a Conduit working environment creating the `conduit.yaml` configuration file,
and the three necessary directories: processors, pipelines, and connectors.

It does not require having a Conduit instance running.

It won't require flags or arguments.

An additional global flag named `--config.path` could specify the path where this configuration will be created.

#### `--help`

```bash
$ conduit init [--config.path PATH]

EXAMPLES
  $ conduit init
  $ conduit init --config.path ~/code/conduit-dir
```

### `conduit config`

This command will output the [Conduit configuration](https://github.com/ConduitIO/conduit/blob/05dbc275a724526f02779abb47b0ecc53f711485/pkg/conduit/config.go#L34)
based on the existing configuration.

This will take into account the default values and the user's configured settings that Conduit will use.

It requires having Conduit running.

#### Flags

| Name | Description | Required | Default Value |
|------|-------------|----------|---------------|
| grpc.address | address for serving the gRPC API | No | ":8084" |

#### `--help`

```bash
$ conduit config

DB ...
API.HTTP: ...
...
```

### `conduit run [--connectors.path] [...]`

This command will run Conduit with all the configured pipelines, connectors, and processors.

It is equivalent to the current `conduit` command as of time of this writing.

Conduit will run based on `--config.path` or current directory. Example:

```bash
$ pwd
/usr/code

$ ls
conduit.yaml
connectors/
pipelines/
processors/
```

Other flags such as `connectors.path`, etc. will overwrite the existing configuration on `conduit.yaml`.
This will need to be evaluated before specifying to Conduit to accomodate both scenarios (absolute and relative paths).

#### Flags

| Name | Description | Required | Default Value |
|------|-------------|----------|---------------|
| connectors.path | path to standalone connectors' directory | No | "./connectors" |
| db.badger.path | path to badger DB | No | "Conduit.db" |
| processors.path | path to standalone processors' directory | No | "./processors" |
| pipelines.path | path to the directory that has the yaml pipeline configuration files, or a single pipeline configuration file | No | "./pipelines" |
| db.postgres.connection-string | postgres connection string | Yes |  |
| db.postgres.table | postgres table in which to store data | No | "Conduit_kv_store" |
| db.type | database type; accepts badger,postgres,inmemory | No | "badger" |
| grpc.address | address for serving the gRPC API | No | ":8084" |
| http.address | address for serving the HTTP API | No | ":8080" |
| log.format | sets the format of the logging; accepts json, cli | No | "cli" |
| log.level | sets logging level; accepts debug, info, warn, error, trace | No | "info" |
| pipelines.exit-on-error | exit Conduit if a pipeline experiences an error while running | No |  |

### `conduit pipelines init [NAME] [--pipelines.path] [--source] [--destination]`

This command will initialize a pipeline based on the working environment. Optionally, a user could provide a different flag
if they want to specify a different path.

In the event of not being able to to read a `conduit.yaml` configuration file based on current directory or `--config.path`,
we should prompt to set up a working Conduit environment via `conduit init`.

It does not require having Conduit running.

A source and a destination could be provided using the same connectors reference as described
[here](https://conduit.io/docs/using/connectors/referencing).

#### Arguments

| Name | Description | Required | Default Value |
|------|-------------|----------|---------------|
| name  |  Pipeline file name and pipeline name  | No | `pipeline-#` (`pipeline-#.yaml`) |

#### Flags

| Name | Description | Required | Default Value |
|------|-------------|----------|---------------|
| destination  |  Plugin name of the destination connector  | No | `log` |
| pipelines.path  |  Where to initialize a new pipeline | No | `.` (current directory) |
| source  |  Plugin name of the source connector  | No | `generator` |

#### `--help`

```bash
conduit pipelines init
conduit pipelines init my-first-pipeline
conduit pipelines init my-first-pipeline --pipelines.path ~/my-other-path
conduit pipelines init --source file@v1.0 --destination file
```

### `conduit pipelines ls`

This command will list the running pipelines.

It requires having Conduit previously running.

#### Flags

| Name | Description | Required | Default Value |
|------|-------------|----------|---------------|
| grpc.address | address for serving the gRPC API | No | ":8084" |

#### `--help`

```bash
$ conduit pipelines ls
NAME            STATUS
my-pipeline     running
my-other        degraded      
```

### `conduit pipelines describe ID`

This command will describe the topology of the pipeline.

It requires having Conduit previously running.

It requires the pipeline id as argument.

#### Arguments

| Name | Description             | Required | Default Value |
|------|-------------------------|----------|---------------|
| id   | pipeline id to describe | Yes      |               |

#### Flags

| Name         | Description                      | Required | Default Value |
|--------------|----------------------------------|----------|---------------|
| grpc.address | address for serving the gRPC API | No       | ":8084"       |

#### `--help`

```bash
$ conduit pipelines describe ID

EXAMPLE:

$ conduit pipelines describe my-pipeline
ID: generator-to-log
State:
  Status: STATUS_STOPPED
Config:
  Name: generator-to-log
  Description: Postgres source, file destination
Connector IDs:
  - generator-to-log:postgres-source
  - generator-to-log:destination
Processor IDs:
  - my-processor:avro.encode
Created At: 2024-10-09T13:55:17.113068Z
Updated At: 2024-10-09T13:55:17.113802Z
```

### `conduit connectors ls`

This command will list all the available connectors.

It requires having Conduit previously running.

#### `--help`

```bash
$ conduit connectors ls
ID                PLUGIN                          TYPE         PIPELINE
my-source         postgres@v0.2.0                 builtin      file-to-postgres
my-destination    conduit-connector-http@0.1.0.   standalone   my-other-pipeline
```

### `conduit connectors describe ID`

This command will describe the existing running connector.

It requires having Conduit previously running.

#### Arguments

| Name | Description              | Required | Default Value |
|------|--------------------------|----------|---------------|
| id   | connector id to describe | Yes      |               |

#### Flags

| Name         | Description                      | Required | Default Value |
|--------------|----------------------------------|----------|---------------|
| grpc.address | address for serving the gRPC API | No       | ":8084"       |

#### `--help`

```bash
$ conduit connnectors describe ID [--grpc.address]

EXAMPLE:

$ conduit connectors describe my-source
NAME        URL             PIPELINE        PLUGIN
my-source   https://...     my-pipeline     postgres@v0.2.0
```

### `conduit connector-plugins ls`

This command will list all the available connector plugins.

It does not require having Conduit previously running.

#### `--help`

```bash
$ conduit connector-plugins ls
PLUGIN                          
postgres@v0.2.0                 
conduit-connector-http@0.1.0
```

### `conduit connector-plugins describe PLUGIN`

This command will describe the configuration for that connector plugin.

It does not require having Conduit previously running.

#### Arguments

| Name   | Description                  | Required | Default Value |
|--------|------------------------------|----------|---------------|
| PLUGIN | connector plugin to describe | Yes      |               |

#### `--help`

```bash
$ conduit connnector-plugins describe PLUGIN

EXAMPLE:

$ conduit connector-plugins describe conduit-connector-http@0.1.0
NAME   DESCRIPTION                       REQUIRED  DEFAULT VALUE  EXAMPLE
url    HTTP URL to send requests to.     true                     https://...
```

### `conduit processors ls`

This command will list all the available processors.

It requires having Conduit previously running.

#### Flags

| Name         | Description                      | Required | Default Value |
|--------------|----------------------------------|----------|---------------|
| grpc.address | address for serving the gRPC API | No       | ":8084"       |

#### `--help`

```bash
$ conduit processors ls
NAME          TYPE      PIPELINE
avro.decode   builtin   my-pipeline
avro.encode    builtin   my-pipeline
base64.decode  builtin   my-other-pipeline
```

### `conduit processors describe ID`

This command will describe the existing running processor.

It requires having Conduit previously running.

#### Arguments

| Name | Description  | Required | Default Value |
|------|--------------|----------|---------------|
| id   | processor ID | Yes      |               |

#### `--help`

```bash
$ conduit processors describe ID

EXAMPLE:

$ conduit processors describe my-processor
NAME            PLUGIN            PIPELINE
my-processor    base64.decode     my-pipeline
```

### `conduit processor-plugins ls`

This command will list all the available processors.

It does not require having Conduit previously running.

#### `--help`

```bash
$ conduit processor-plugins ls
NAME
avro.decode
avro.encode
base64.decode
```

### `conduit processor-plugins describe PLUGIN`

This command will describe the processor plugin configuration available.

It does not require having Conduit previously running.

#### Arguments

| Name   | Description           | Required | Default Value |
|--------|-----------------------|----------|---------------|
| plugin | processor plugin name | Yes      |               |

#### `--help`

```bash
$ conduit processor-plugins describe PLUGIN

EXAMPLE:

$ conduit processor-plugins describe avro.decode
# auth.basic.password
type: string
description: This option is required if auth.basic.username contains a value. If both auth.basic.username and auth.basic.password are empty basic authentication is disabled.

# auth.basic.username
type: string
...
```

### `conduit version`

This will return the existing Conduit version.

It requires having Conduit previously running.

#### `--help`

```bash
conduit version
```

#### Global flags

| Name        | Description                                                 | Required | Default Value |
|-------------|-------------------------------------------------------------|----------|---------------|
| config.path | path to the Conduit working environment                     | No       | `.`           |
| json        | output json                                                 | No       |               |
| version     | prints current Conduit version (alias to `conduit version`) | No       |               |
