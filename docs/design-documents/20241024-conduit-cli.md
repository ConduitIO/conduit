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

## Suggested commands for Conduit 0.13

The following list contains the suggested commands we propose to include in the first iteration. New and more complex commands will be added later.

<details>
<summary><code style="font-size: 19px; font-weight:bold;">conduit init</code></summary>

#### Description

- This command will initialize a Conduit working environment creating the `conduit.yaml` configuration file, and the three directories: processors, pipelines, and connectors.
- It does not require having conduit running.
- It won't require flags or arguments.
- Additional flags could be provided to specify the path.

#### Flags

|Flag name | Description | Required  | Default  |
|---|---|---|---|
| path  |  Where to initialize Conduit | No | `.` (current directory) |

#### `--help`

```bash
$ conduit init [--path PATH]

EXAMPLES
  $ conduit init
  $ conduit init --path ~/code/conduit-dir
```

</details>

<br/>

<details>
<summary><code style="font-size: 19px; font-weight:bold;">conduit config</code></summary>

#### Description

- This command will output the [Conduit configuration](https://github.com/ConduitIO/conduit/blob/05dbc275a724526f02779abb47b0ecc53f711485/pkg/conduit/config.go#L34) based on the default values and the user's configured settings that Conduit would use.
- It does not require having conduit running.


#### `--help`

```bash
$ conduit config
```

</details>

<br/>

<details>
<summary><code style="font-size: 19px; font-weight:bold;">conduit run [...]</code></summary>

#### Description

- This command will run Conduit with all the configured pipelines, connectors, etc.
- It is equivalent to the current `conduit` command.
- `config.path` will be the root of the working enviornment. Example:

```bash
$ pwd
/usr/code

$ ls
conduit.yaml
connectors/
pipelines/
processors/
```

- Other flags such as `connectors.path`, etc. will overwrite the existing configuration on `conduit.yaml`. This will need to be evaluated before specifying to conduit to accomodate both scenarios (absolute and relative paths). 

#### Flags

| Name | Description | Required | Default Value |
|------|-------------|----------|---------------|
| connectors.path | path to standalone connectors' directory | No | "./connectors" |
| db.badger.path | path to badger DB | No | "conduit.db" |
| processors.path | path to standalone processors' directory | No | "./processors" |
| pipelines.path | path to the directory that has the yaml pipeline configuration files, or a single pipeline configuration file | No | "./pipelines" |
| db.postgres.connection-string | postgres connection string | Yes |  |
| db.postgres.table | postgres table in which to store data | No | "conduit_kv_store" |
| db.type | database type; accepts badger,postgres,inmemory | No | "badger" |
| grpc.address | address for serving the gRPC API | No | ":8084" |
| http.address | address for serving the HTTP API | No | ":8080" |
| log.format | sets the format of the logging; accepts json, cli | No | "cli" |
| log.level | sets logging level; accepts debug, info, warn, error, trace | No | "info" |
| pipelines.exit-on-error | exit Conduit if a pipeline experiences an error while running | No |  |

#### `--help`

```bash
$ conduit run
```
</details>

<br/>

<details>
<summary><code style="font-size: 19px; font-weight:bold;">conduit pipelines init [NAME] [--pipelines.path]</code></summary>

#### Description

- This command will initialize a pipeline in the previously configured pipelines path (existing on `conduit.yaml`). In other words, even if you aren't on the pipelines directory, this pipeline will be initialized there.
- In the event of not having a `conduit.yaml` configuration file already, we should prompt to initialize a working conduit environment.
- It does not require having conduit running.

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
$ conduit pipelines init
$ conduit pipelines init my-first-pipeline
$ conduit pipelines init my-first-pipeline --pipelines.path ~/my-other-path
$ conduit pipelines init --source file@v1.0 --destination file
```

</details>

<br/>

<details>
<summary><code style="font-size: 19px; font-weight:bold;">conduit pipelines ls</code></summary>

#### Description

- This command will list the running pipelines.
- It requires having conduit previously running.

#### Flags

| Name | Description | Required | Default Value |
|------|-------------|----------|---------------|
| grpc.address | address for serving the gRPC API | No | ":8084" |

#### `--help`

```bash
$ conduit pipelines ls
```
</details>

<br/>

<details>
<summary><code style="font-size: 19px; font-weight:bold;">conduit pipelines describe [ID]</code></summary>

#### Description

- This command will describe the topology of the pipeline.
- It requires having conduit previously running.
- It requires the pipeline id as argument.

#### Arguments

| Name | Description | Required | Default Value |
|------|-------------|----------|---------------|
| id  |  pipeline id to describe | Yes | |
| grpc.address | address for serving the gRPC API | No | ":8084" |


#### `--help`

```bash
$ conduit pipelines describe [ID]

EXAMPLE:

$ conduit pipelines describe my-pipeline
Source: kafka
  Processor: avro.encode
Destination: kafka
```
</details>

<br/>

<details>
<summary><code style="font-size: 19px; font-weight:bold;">conduit connectors ls</code></summary>

#### Description

- This command will list all the available connectors.
- It requires having conduit previously running.

#### `--help`

```bash
$ conduit connectors ls
PLUGIN                          TYPE         PIPELINE
postgres@v0.2.0	                builtin      file-to-postgres
conduit-connector-http@0.1.0.   standalone   my-other-pipeline
```
</details>

<br/>

<details>
<summary><code style="font-size: 19px; font-weight:bold;">conduit connectors describe [PLUGIN]</code></summary>

#### Description

- This command will describe the connector configuration available.
- It requires having conduit previously running.

#### Arguments

| Name | Description | Required | Default Value |
|------|-------------|----------|---------------|
| plugin  |  plugin name and version | Yes | |
| grpc.address | address for serving the gRPC API | No | ":8084" |


#### `--help`

```bash
$ conduit connnectors describe [PLUGIN]

EXAMPLE:

$ conduit connectors describe conduit-connector-http@0.1.0
NAME   DESCRIPTION                       REQUIRED  DEFAULT VALUE	EXAMPLE
url    HTTP URL to send requests to.     true		                 https://...
...
```
</details>

<br/>

<details>
<summary><code style="font-size: 19px; font-weight:bold;">conduit processors ls</code></summary>

#### Description

- This command will list all the available processors.
- It requires having conduit previously running.

#### `--help`

```bash
$ conduit processors ls
NAME			    TYPE      PIPELINE
avro.decode 	builtin   my-pipeline
avro.encode		builtin   my-pipeline
base64.decode	builtin   my-other-pipeline
```
</details>
<br/>

<details>
<summary><code style="font-size: 19px; font-weight:bold;">conduit processor describe [NAME]</code></summary>

#### Description

- This command will describe the processor configuration available.
- It requires having conduit previously running.
- It requires the processor name as argument.

#### Arguments

| Name | Description | Required | Default Value |
|------|-------------|----------|---------------|
| name  |  processor name | Yes | |



#### `--help`

```bash
$ conduit processor describe [NAME]

EXAMPLE:

$ conduit connectors describe avro.decode
# auth.basic.password
type: string
description: This option is required if auth.basic.username contains a value. If both auth.basic.username and auth.basic.password are empty basic authentication is disabled.

# auth.basic.username
type: string
...
```
</details>

<br/>

<details>
<summary><code style="font-size: 19px; font-weight:bold;">conduit doctor</code></summary>

#### Description

- This will check whether there’s a more up to date version of conduit and if some connectors / processors could also be updated.
- It requires having conduit previously running.

#### `--help`

```bash
$ conduit doctor
# returns version, checks if there's a newer version of conduit, plugin versions, etc.
```
</details>

<br/>

<details>
<summary><code style="font-size: 19px; font-weight:bold;">conduit version</code></summary>

#### Description

- This will return the existing Conduit version.
- It requires having conduit previously running.

#### `--help`

```bash
$ conduit version
```
</details>

<br/>

#### Global flags

| Name | Description | Required | Default Value |
|------|-------------|----------|---------------|
| config.path | path to the conduit working environment | No | "." |
| version | prints current Conduit version (alias to `conduit version`) | No |  |


<!-- 
| grpc.address | address for serving the gRPC API | No | ":8084" |
-->


<!-- 
| connectors.path | path to standalone connectors' directory | No | "./connectors" |
| pipelines.path | path to the directory that has the yaml pipeline configuration files, or a single pipeline configuration file | No | "./pipelines" |
| processors.path | path to standalone processors' directory | No | "./processors" | -->


### TBD

1. `config` vs `doctor`? 
1. Plugins vs Names (for connectors and processors)