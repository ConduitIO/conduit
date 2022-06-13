# Loading Pipeline Configuration Files

## Summary 

This document describes the design and implementation for loading pipeline 
configuration files. It details how Conduit loads configuration files at start.

In order to provide a detailed history of configuration changes, better secret
handling, and increased confidence in a given configuration, we propose
adding support for loading and subsequently running pipelines entirely from 
configuration files loaded into Conduit from a configured directory.

Our solution proposes an immutable Conduit runtime backed by an in-memory DB
implementation that merges a user-provided configuration file with environment
variables and CLI flags at runtime to seed Conduit's services with the necessary 
information for full operation. 

## Goals 

- Fully load and configure pipelines from a directory of pipeline files.
- Properly handle environment variable injection into that file.

### Non Goals

Defining what we don't want out of this design and implementation is equally 
important as what we _do_ want.

- We don't care about the structure or schema of configuration files.
- We shouldn't implement key management or encryption for secrets.
- We shouldn't care or design for how environment variables are set or managed.

## Scope

We are strictly concerned with the loading of pipeline files at start time and 
how those files are handled through Conduit's full operation cycle.

Configuration file schema and other file-level design choices are out of scope.

This document doesn't make any definitions or statements about how the UI should
handle these changes. Those should be reserved for later design documents and
discussion.

## Key Terms 

**pipeline** - a Conduit pipeline with `n` number of Source connectors, 
`m` number of Processors, and `o` number of Destination connectors.

**production mode** - a mode of operation where Conduit pipelines must be
statically defined and cannot change after Conduit starts. No UI is served in
production mode, and only pipeline lifecycle methods can be called over the
API.

## Context 

Running Conduit in production environments requires a strict set of
guarantees. Manually configuring a Conduit server in a GUI and keeping those
high standards places more work on the end user, reduces confidence in the
pipeline's integrity, and in the worst case could result in corrupted or lost
data.

This document expands on previous discussions to create a production mode of
Conduit that either doesn't serve the application GUI or serves a functionally 
limited version of the GUI instead.

Conduit Issues #32 and #33 describe two different features to address some of 
these concerns, but there are additional design decisions to make.

## Design 

We propose the creation of a pipeline configuration directory that contains
configuration values needed for `n` number of full pipelines.

We propose scanning for `*.yml` files in this directory and loading them into
Conduit.

If files are detected at start time, we propose that Conduit run in production 
mode. Production mode initially will not serve the UI. In the future, it would 
serve a read-only optimized view of Conduit. If no pipeline files are detected
then Conduit would boot into it's standard development mode of operation.

Configuration must securely accept secret information. We don't want to solve
the problem of secrets management, but we must be in the business of secret
concealment. Additionally, we already allow environment variables so we must 
continue handling those correctly. 

We propose merging CLI flags, environment variables, and the user's provided 
configuration file, in that order, at runtime into template variables in the 
configuration file with their secret values at runtime.

We propose merging environment variables and CLI flags into a set of values 
that is then injected into the provided set of pipeline files.

We propose supporting Go templating in all pipeline files to allow for dynamic
injection of those environment variables at runtime.

## Implementation

Conduit currently defines a `Config` struct in `pkg/conduit` that it 
accepts at runtime to configure behavior. 

We propose modifying this configuration struct to allow for configuration
files to be loaded f

```go
// Config holds all configurable values for Conduit.
type Config struct {
	// Pipelines holds a path to a directory that Conduit scans at start time 
  // to detect and load pipelines.
	Pipelines struct {
		Path string
	}

	DB struct {
		Type   string
		Badger struct {
			Path string
		}
		Postgres struct {
			ConnectionString string
			Table            string
		}
	}

	HTTP struct {
		Address string
	}
	GRPC struct {
		Address string
	}

	Log struct {
		Level  string
		Format string
	}
}
```

The above configuration is parsed at start time and provies a reference to our 
pipeline configuration file directory. After parsing that file, we inject 
environment variables into their corresponding templated locations, if any.

```go
// DB defines the interface for a key-value store.
type DB interface {
	// NewTransaction starts and returns a new transaction.
	NewTransaction(ctx context.Context, update bool) (Transaction, context.Context, error)
	// Close should flush any cached writes, release all resources and close the
	// store. After Close is called other methods should return an error.
	Close() error

	Set(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	GetKeys(ctx context.Context, prefix string) ([]string, error)
}
```

### Configuration driver

`DB` is an interface for storing key-value pairs. Our `ConfigurationDriver` 
struct will store a reference to a parsed configuration file and 
environment and maps that to a `Store` for each `Service`.

A key part about `ConfigurationDriver` is that it stubs out mutable
operations, including transaction handling, enforcing our immutability
during operation.

We pass that `ConfigurationDriver` to each new service at start time.
Our `ConfigurationDriver` will be bound with an in-memory database and passed
to each new service when Conduit is starting.

The `ConfigurationDriver` will still pass a valid database reference to the 
ConnectorPersister so that connectors can persist position information.

```go
// NewService initializes and returns a pipeline Service.
func NewService(logger log.CtxLogger, db database.DB) *Service {
	return &Service{
		logger:        logger.WithComponent("pipeline.Service"),
		store:         NewStore(db),
		instances:     make(map[string]*Instance),
		instanceNames: make(map[string]bool),
	}
}

svc := NewService(logger, &ConfigurationDriver{})
```

### Hydration 

Once Conduit's services are started, they will be empty. We must parse our 
pipeline files into a full pipeline by hydrating it with other necessary 
information including environment information, connector configurations, 
and processors.

This step will be responsible for populating the service's individual `Stores` 
with the necessary resources before `Init` is called during start.

This process can fail, in which a pipeline reports `StatusDegraded`.
Because we aren't dictating restart behavior in configuration files, this
should cause Conduit to fail the pipeline and report the errors it received.

### Immutability

Immutability provides stronger guarantees to failure resilience and production
operations. Conduit should be immutable at runtime to take advantage of these
benefits.

We could achieve this by stubbing out all mutable methods as discussed in 
previous sections. However, the `ConnectorPersister` must be able to persist 
information between restarts.

Since we must persist connector information, but we want Conduit services 
to be immutable, we should pass the `ConnectorPersister` its own database
reference at start that it can mutate without changing Conduit's state. 

## Lifecycle methods 

Configuration files should specify whether or not a pipeline should run at
startup, but should not dictate restart behavior. 

## Open questions

- Do we ever want to support generating config files from Conduit's other data
stores?
  - What would this look like? A manual setup and configuration process to get
  a pipeline working and then export it?
  - It seems this is a feature we could support, so we should keep it in mind.

- Should we call the file `conduit.yml` or `pipelines.yml`
  - `conduit` implies application-level configuration values, while `pipelines` 
  implies a tighter scope.

- Do we have any objection to using `yml` as our default file format?
  - We could add support for other formats, but we should pick only one to 
  start.

- How should Conduit handle degraded states in a production-mode environment?
  - It should do as it currently does: Report the error status and fail the
  pipeline.

## Do nothing 

We must always consider the case where we do nothing. 

- What are the consequences of us never supporting pipeline configuration and
management from files?
- What if we completely ignore secrets handling and require them to be loaded
into a file for simplicity? 

**Pros**: 

- If we don't implement pipeline configuration files now, we can focus on other
aspects of Conduit like performance, acceptance testing, etc...
- We maintain a simpler runtime with only one operation mode.

**Cons**:

- We don't see the benefits of stricter runtime definitions.
- Production Conduit instances remain manually configured by default.

## Decision 

Decision points from this document:

- We should allow `n` number of pipeline files to be scanned and loaded.
   - These will live in a sub-directory by default.
   - If there are no files, Conduit will start as normal in development mode.
- We want to eventually support exporting pipeline configuration files from a
development instance of Conduit.
- We shouldn't care or design for how environment variables are set or managed.
This design is only concerned with their values, not how they're set.

## Consequences

How Conduit is used and run in production, staging, and development 
environments will have long-term consequences for its adoption and use.

Beyond that, technical consequences include:

- Increased complexity of a second operational mode.
- Pipeline configurations can be checked into version control.

## Related 

[Issue #33: Load pipeline config files on startup](https://github.com/ConduitIO/conduit/issues/33)
