# Loading Pipeline Configuration Files

## Summary 

This document describes the design and implementation for loading pipeline 
configuration files. It details how Conduit loads a configuration file at start.

In order to provide a detailed history of configuration changes, better secret
handling, and increased confidence in a given configuration, we propose
adding support for loading and subsequently running pipelines entirely from 
configuration files loaded into Conduit at its root or a configured directory.

Our solution proposes an immutable Conduit runtime backed by an in-memory DB
implementation that merges a user-provided configuration file with environment
variables and other information at runtime to seed Conduit's services with 
the necessary information for full operation. 

## Goals 

- Fully load and configure pipelines from a file.
- Properly handle environment variable injection into that file.

### Non Goals

Defining what we don't want out of this design and implementation is equally 
important as what we _do_ want.

- We don't care about the structure or schema of configuration files.
- We shouldn't implement key management or encryption for secrets.

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

We propose the creation of a pipeline configuration file that contains all of
the configuration values needed for `n` number of full pipelines.

We propose naming this file `pipelines.yml` and it's default location should be
at the root of the project. The format of the file is to be determined, though
`yaml` has been proposed previously.

If this file is passed at start time, we propose that Conduit run in production 
mode. Production mode initially will not serve the UI. In the future, it would 
serve a read-only optimized view of Conduit.

Configuration must securely accept secret information. We don't want to solve
the problem of secrets management, but we must be in the business of secret
concealment. Additionally, we already allow environment variables so we must 
continue handling those correctly. 

We propose merging environment variables, CLI flags, and the user's provided 
configuration file, in that order, at runtime into a templated variables in the 
configuration file with their secret values at runtime.

We propose merging environment variables and CLI flags into a set of values 
that is then injected into the provided `pipelines.yml` file. 

We propose supporting Go templating in `pipelines.yml` to allow for dynamic
injection of those environment variables at runtime.

## Implementation

Conduit currently defines a `Config` struct in `pkg/conduit` that it 
accepts at runtime to configure behavior. 

We propose modifying this configuration struct to allow for a configuration
file to be set for Conduit to read.

```go
// Config holds all configurable values for Conduit.
type Config struct {
	// File holds a Path to a configuration file that Conduit should load.
	File struct {
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
pipeline configuration file. After parsing that file, we inject environment 
variables into their corresponding templated locations, if any.

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

### Config files as a database implementation

`DB` is an interface for storing key-value pairs. Our `ConfigFileAsDatabase` 
struct will store a reference to a parsed configuration file and 
environment and maps that to a `Store` for each `Service`.

A key part about `ConfigFileAsADatabase` is that it stubs out mutable
operations, including transaction handling, enforcing our immutability
during operation.

We pass that `ConfigFileAsADatabase` to each new service at start time.
Our `ConfigFileAsDatabase` will be bound with an in-memory database and passed
to each new service when Conduit is starting.

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

svc := NewService(logger, &ConfigAsDatabase{})
```

### Hydration 

Once the services are started, they will be empty. We must parse our config 
file into a full pipeline by hydrating it with other necessary information 
including environment information, connector configurations, and processors.

This step will be responsible for populating the service's individual Stores
with the necessary resources before `Init` is called during start.

### Immutability

Conduit should be immutable at runtime. We could achieve this by stubbing 
out all mutable methods as discussed in previous sections. However, the 
ConnectorPersister must be able to persist information between restarts.
To maintain the separation, we should pass the Connector Persister

The Connector Persister also stores connector information in the database. 
Since we must persist connector information, but we want Conduit services 
to be immutable, we should pass the `Persister` its own separate database.

## Open questions

- Do we ever want to support generating config files from Conduit's other data
stores?
  - What would this look like? A manual setup and configuration process to get
  a pipeline working and then export it?

- Should we call the file `conduit.yml` or `pipelines.yml`
  - `conduit` implies application-level configuration values, while pipelines 
  implies a tighter scope.

- Do we have any objection to using `yml` as our default file format?
  - We could add support for other formats.

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

- Production Conduit instances remain manually configured by default.

## Decision 

> T.B.D.

## Consequences

How Conduit is used and run in production, staging, and development 
environments will have long-term consequences for its adoption and use.

Beyond that, technical consequences include:

- Increased complexity of a second operational mode.
- Pipeline configurations can be checked into version control.

## Related 

[Issue #33: Load pipeline config files on startup](https://github.com/ConduitIO/conduit/issues/33)
