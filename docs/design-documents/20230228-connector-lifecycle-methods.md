# Connector Lifecycle Methods

## Introduction

Connector Lifecycle Methods are additional integration points that would let
connectors execute certain operations when the connector is first created, when
its configuration is updated or when it gets deleted.

### Goals

- Lifecycle methods should be optional to implement.
- The introduction of lifecycle methods must be backwards compatible with
  existing connectors.
- The connector needs to be notified when it was first created*.
- The connector needs to be notified when its configuration has changed*.
- The connector needs to be notified when it gets deleted.

\*Actually when the pipeline is first started after that happened.

### Questions

- **The connector method `Configure` is similar to lifecycle methods when the
  connector is created or updated. Should we reuse that method?**
  - Probably not, we need more information in lifecycle methods (e.g. old
    configuration before update) but we don't want to break the existing
    interface. Also, the purpose of `Configure` is different from the purpose of
    lifecycle methods.
- **Should the config be validated before passed to the lifecycle method?**
  - This is probably not needed if lifecycle methods are triggered _after_
    `Configure` is called (it will ensure the configuration is valid). In case
    of an update event, we know the old configuration was valid because it was
    already once used to successfully configure the connector.
- **Should the connector method `Configure` be called before a deletion
  lifecycle method?**
  - It's a trick question - we can't really do that, because the "new"
    configuration that we would need to pass to `Configure` is empty which
    _probably_ makes it invalid. This needs to be called out explicitly in the
    SDK documentation.

### Background

We first saw a need for this in the Postgres connector, which can create and
manage its own logical replication slot. We run into a cleanup problem - if the
logical replication slot is created by the connector it must also be deleted by
the connector, so the connector currently deletes it in `Teardown`. This defeats
the whole purpose of a logical replication slot because the connector loses all
state. Next time it starts it will create a new logical replication slot and
miss all changes that happened while the pipeline was not running.

Introducing lifecycle methods would allow the connector to create the logical
replication slot the first time the pipeline runs and remove it once the
connector gets deleted. Or more generally - lifecycle methods would allow a
connector to execute initializations on creation and clean up after itself on
deletion. Additionally, we need to take into account that the configuration can
change between runs and we need to notify the connector about these changes.

## Implementation options

All options require changes to the connector protocol, the SDK and Conduit
itself.

### Option 1 - Separate methods

We can introduce separate methods for each lifecycle event. There are 3 new
methods, each signaling one of these events:

- connector was created
- connector was updated
- connector was deleted

In the connector protocol we add 3 new methods to services `SourcePlugin`
and `DestinationPlugin`:

```protobuf
service SourcePlugin/DestinationPlugin {
    rpc OnCreate(Source.OnCreate.Request) returns (Source.OnCreate.Response);
    rpc OnUpdate(Source.OnUpdate.Request) returns (Source.OnUpdate.Response);
    rpc OnDelete(Source.OnDelete.Request) returns (Source.OnDelete.Response);
}
```

The requests carry the old and new configuration (in case of `OnCreated` only
the new one, in case of `OnDeleted` only the old one).

In the SDK we add 3 methods to the `Source` and `Destination` interfaces:

```go
type Source/Destination interface {
    ConnectorCreated(ctx context.Context, config map[string]string) error
    ConnectorUpdated(ctx context.Context, oldConfig map[string]string, newConfig map[string]string) error
    ConnectorDeleted(ctx context.Context, oldConfig map[string]string) error
}
```

Backwards compatibility with existing connectors is ensured through
[`UnimplementedSource`](https://github.com/ConduitIO/conduit-connector-sdk/blob/edae165f49975c04fb5d9e549ea1987060068fc2/unimplemented.go#L49)
and [`UnimplementedDestination`](https://github.com/ConduitIO/conduit-connector-sdk/blob/edae165f49975c04fb5d9e549ea1987060068fc2/unimplemented.go#L20),
which all sources and destinations need to embed.

#### Pros

- Methods are clearly separated.
- We can separately evolve each lifecycle method request/response in the future.

#### Cons

- Verbose.

### Option 2 - Single method

Since all lifecycle methods have a similar purpose and method signature we could
collapse them into a single method. The purpose is to notify the connector that
its configuration changed since it last ran.

In the connector protocol we add 1 new method to services `SourcePlugin`
and `DestinationPlugin`:

```protobuf
service SourcePlugin/DestinationPlugin {
    rpc OnConfigChange(Source.OnConfigChange.Request) returns (Source.OnConfigChange.Response);
}
```

The requests carry the old and new configuration. In case the connector is run
for the first time, the old configuration will be `nil`, if the connector is
getting deleted the new configuration will be `nil`, in case of an update both
configurations will be populated (old contains the last active config values,
new contains the latest config values).

In the SDK we add 1 method to the `Source` and `Destination` interfaces:

```go
type Source/Destination interface {
    ConfigChanged(ctx context.Context, oldConfig map[string]string, newConfig map[string]string) error
}
```

Backwards compatibility with existing connectors is ensured through
[`UnimplementedSource`](https://github.com/ConduitIO/conduit-connector-sdk/blob/edae165f49975c04fb5d9e549ea1987060068fc2/unimplemented.go#L49)
and [`UnimplementedDestination`](https://github.com/ConduitIO/conduit-connector-sdk/blob/edae165f49975c04fb5d9e549ea1987060068fc2/unimplemented.go#L20),
which all sources and destinations need to embed.

#### Pros

- Simpler design.
- The logic for all three lifecycle events will probably be related, so it's
  combined in one method.

#### Cons

- We risk having to add some data to one lifecycle event in the future and are
  then forced to change it for the other ones as well (or pass `nil`).
- The connector developer needs to figure out if the config was created, updated
  or deleted.

### Option 3 and 4

We can combine the approaches described in option 1 and option 2 by choosing one
for the connector protocol and the other for the SDK.

#### Pros

- Choose whatever approach makes sense in each part of the integration (protocol
  and SDK).

#### Cons

- Looser connection between the protocol and SDK (different abstraction levels).

### Common to all options

The logic in Conduit will need to be adjusted so that lifecycle methods are
triggered. When should the lifecycle methods be called? If we call them
before `Configure` we risk that `Configure` later returns an error (i.e. config
is invalid) but we still executed the initialization. On the other hand,
if `Configure` was already called the lifecycle methods don't have to parse the
config itself and they can know that the config is valid. That's why we should
call lifecycle methods _after_ `Configure` and before `Open` (at that point the
initialization already needs to be done).

The lifecycle methods should be called at the following points:

- Connector creation - should be called the first time the pipeline is started
  after the connector was created.
- Connector update - should be called the first time the pipeline is started
  after the connector was updated (i.e. it was started once before already).
- Connector deletion - should be called when the connector gets deleted. If it
  fails, the error _should not_ prevent the deletion of the connector.

We need to define which configuration gets passed to lifecyle methods. For this
let's define that a connector configuration is "active" only after a lifecycle
method was called with that configuration and no error was returned.
Consequently, there are some edge cases we need to look out for:

- If a new connector is created and deleted without being started in between,
  the configuration is not treated as "active" so the deletion lifecycle method
  won't be called because the creation was not triggered either (it would only
  be triggered if the connector was actually started).
- If a new connector is created and updated without being started in between,
  the configuration passed to the creation lifecycle method should be the latest
  one (after the update).
- If a connector is updated twice without being started between updates, the
  update lifecycle method should be called with the last active configuration
  (old) and with the latest configuration (new).
- If a connector is updated and deleted without being started in between, the
  deletion lifecycle method should receive the old configuration before the
  update.
- If a connector is not configured correctly (i.e. `Configure` returns an
  error), then no lifecycle method is called and that configuration should not
  be treated as active (unless it was already active before).

If a pipeline is provisioned using a pipeline configuration file we need to make
sure to call the correct connector lifecycle method based on if the
configuration actually changed (we need to make a diff). Similarly, if a
connector gets removed from the configuration file or the configuration file is
removed entirely, the deletion lifecycle method should be called (given that the
connector was active before).

We need to make sure that Conduit stays backwards compatible with existing
standalone connectors, which means we need to catch errors in case the newly
introduced methods in the connector protocol do not exist.

### Recommendation

Option 1 seems like the most straightforward option with enough freedom to
evolve the design in the future.
