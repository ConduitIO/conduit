# Conduit CLI

## Introduction

The goal of this design document is to clarify which part of this [Conduit discussion](https://github.com/ConduitIO/conduit/discussions/1642) will be part of the upcoming [0.13 release](https://github.com/ConduitIO/conduit/milestone/15). The intention is to provide a good starting experience and evolve it over time with more complex and advanced features.

### Goals

- We aim to provide our users with an interface that empowers them to use the **main** Conduit features effortlessly. While we have envisioned some complex features, they will be available in future releases.
- We will maintain the current installation methods by incorporating the CLI through the same established processes.
- The CLI needs to feel intuitive and align with our standards.

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

## Implementation plan

### `conduit init`

- This command will initialize a conduit pipeline that demonstrates some basic features of Conduit. 
- It won't require flags or arguments.
- Additional flags could be provided to specify a source and a destination
- Optional argument will be for

#### Arguments

|Argument name | Description  | Required  |
|---|---|---|
| Name  | Pipeline name | No |

#### Flags

|Flag name | Description | Required  |
|---|---|---|
| source  |  Connector name | No |
| destination  |   |  No |


#### `--help`

```bash
$ conduit init [NAME] [--source plugin@version] [--destination plugin@version]

EXAMPLES
  $ conduit init
  $ conduit init my-first-pipeline
```


### Global flags
