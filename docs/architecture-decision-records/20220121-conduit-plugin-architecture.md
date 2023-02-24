# Conduit Plugin Architecture

## Summary

This document describes the design for the Conduit plugin architecture. It spans from how Conduit communicates with built-in
and standalone plugins, where and how the interface is defined, how the plugin SDK is defined.

## Context

We want to provide a good development experience to plugin developers, we also want to be able to ship Conduit with real
built-in plugins (compiled into the Conduit binary), we want to make it as easy as possible to write plugins in any
programming language and we want the SDK to be decoupled from Conduit and be able to change without changing Conduit
itself.

## Decision

First, an overview of the decision:

Conduit will internally define a plugin interface that will consume and produce internal structs, making it easy to use
internally. We will have a plugin registry that will know how to dispense a plugin based on its identifier (e.g. plugin
name). The plugin registry will produce two different implementations of the plugin interface, one that runs the plugin
in a separate process and communicates with it through gRPC, and one that calls the plugin directly as if it was a Go
library compiled into the Conduit binary. The plugin interface will be defined in gRPC and won't be aware of internal
Conduit structs at all. The plugin SDK will build on that interface, it will not depend on Conduit, hide implementation
details and provide utilities to make plugin development as easy as possible.

![Plugin Architecture - Simple](https://user-images.githubusercontent.com/8320753/153413544-8702081f-6afe-417d-9685-71e3fa53eac9.svg)

The decision can be broken up into 4 parts, these are explained in detail later on in the document:

- We defined the plugin interface in gRPC
- We defined the interface used internally in Conduit to communicate with plugins
- We introduced plugin registries to support built-in and standalone plugins
- We defined the interface for the plugin SDK

### Conduit plugin interface (gRPC)

The Conduit plugin interface is defined in gRPC, is standalone (does not depend on Conduit or the SDK) and lives
in <https://github.com/conduitio/connector-plugin>. This repository is the only common thing between Conduit and a plugin,
meaning that Conduit uses the interface to interact with plugins and plugins implement the interface.

The proto files define the messages and gRPC interface that needs to be implemented by the connector plugin. The
interface is uploaded to the [buf schema registry](https://buf.build/conduitio/connector-plugin), which allows us to
easily generate the code in any programming language. For Go we
use [remote generation](https://docs.buf.build/bsr/remote-generation/overview) so we don’t have to manually generate any
code.

Alongside the proto files, the repository contains helper structs and methods that hide the gRPC implementation from the
user without changing any semantics. The plugin SDK does not need to know that the communication happens through gRPC.

There are 3 gRPC services that a plugin needs to implement - `SourcePlugin`, `DestinationPlugin` and `SpecifierPlugin`.
Notice that request/response messages are defined in a very verbose way to give us the ability to adapt them in the
future as we see fit without creating a whole new plugin interface.

The source plugin interface looks as follows:

```protobuf
service SourcePlugin {
  rpc Configure(Source.Configure.Request) returns (Source.Configure.Response);
  rpc Start(Source.Start.Request) returns (Source.Start.Response);
  rpc Run(stream Source.Run.Request) returns (stream Source.Run.Response);
  rpc Stop(Source.Stop.Request) returns (Source.Stop.Response);
  rpc Teardown(Source.Teardown.Request) returns (Source.Teardown.Response);
}
```

Functions will be called in the order in which they are defined.

- `Configure` - the plugin needs to validate the configuration it receives and either store the configuration and return
  no error or discard it and return an error explaining why the configuration is invalid. This function serves two purposes:
  - Config validation - Conduit calls `Configure` when the connector is first created or when the configuration is
    updated to validate the configuration. In this case the next call is `Teardown` and the plugin is stopped.
  - Configuring the plugin - Conduit calls `Configure` when the pipeline is started to provide the plugin with its
    config. The next call after a successful response is `Start`.
- `Start` - with a call to this function Conduit signals to the plugin that it wants it to start running. The request
  will contain the position at which the plugin should start running (the position might be empty in case the pipeline
  is started for the first time). The plugin is expected to open any connections needed to fetch records. In case of a
  successful response, the next call will be `Run`.
- `Run` - the important thing to notice here is that this function opens a bidirectional stream between Conduit and the
  plugin. Both streams are independent of each other so this design introduces concurrency. The plugin is expected to
  stream records to Conduit through the response stream and Conduit will asynchronously provide acknowledgments back to
  the plugin each time a record is successfully processed (i.e. the record reaches all destinations successfully).
  Acknowledgments should be used by the plugin to keep track of how many records are still in flight (this is handled by
  the SDK) and can be optionally used to send acknowledgments back to the origin (e.g. some message brokers expect to
  receive acknowledgments). The stream should stay open until either an error occurs or the `Stop` function is called *
  and* all remaining acknowledgments are received (this is handled by the SDK).
- `Stop` - Conduit signals to the plugin it should stop fetching new records from the origin. The plugin is still
  allowed to produce records in the response stream of `Run` in case there are any cached records in memory, but it
  should not go and fetch new ones from the origin.
- `Teardown` - this is the last function called before the plugin will be stopped completely. This is where the plugin
  should stop any open connections and make sure everything is cleaned up and ready for a graceful shutdown. This
  function will be called after `Run` stops running and the streams are closed.

The destination plugin interface looks similar to the source plugin interface:

```protobuf
service DestinationPlugin {
  rpc Configure(Destination.Configure.Request) returns (Destination.Configure.Response);
  rpc Start(Destination.Start.Request) returns (Destination.Start.Response);
  rpc Run(stream Destination.Run.Request) returns (stream Destination.Run.Response);
  rpc Stop(Destination.Stop.Request) returns (Destination.Stop.Response);
  rpc Teardown(Destination.Teardown.Request) returns (Destination.Teardown.Response);
}
```

Functions will be called in the order in which they are defined.

- `Configure` - see the description of `Configure` for the source plugin (same purpose).
- `Start` - Conduit signals to the plugin it wants it to start running. The plugin is expected to open any connections
  needed to write records. In case of a successful response, the next call will be `Run`.
- `Run` - this function opens a bidirectional stream between Conduit and the plugin, same as in the source plugin,
  except that in this case Conduit is sending records to the plugin and receiving acknowledgments back. Both streams are
  independent and are able to transmit data concurrently. The plugin is expected to send an acknowledgment back to
  Conduit for every record it received, even if the record was not processed successfully (in that case the
  acknowledgment should contain the error). The stream should stay open until either an error occurs or the `Stop`
  function is called _and_ all remaining acknowledgments are sent (this is handled by the SDK).
- `Stop` - Conduit signals to the plugin that there be no more records written to the request stream in `Run`. The
  plugin needs to flush any records that are cached in memory, send back the acknowledgments and stop the `Run`
  function.
- `Teardown` - see the description of `Teardown` for the source plugin (same purpose).

Last but not least, the specifier interface:

```protobuf
service SpecifierPlugin {
  rpc Specify(Specifier.Specify.Request) returns (Specifier.Specify.Response);
}
```

This one is very simple and allows the plugin to describe itself. Through this Conduit will get to know the name of the
plugin, version, author, description and data about what parameters the plugin expects in the configuration.

### Conduit internal plugin interface

Conduit internally defines its own plugin interfaces:

```go
type SourcePlugin interface {
  Configure(context.Context, map[string]string) error
  Start(context.Context, record.Position) error
  Read(context.Context) (record.Record, error)
  Ack(context.Context, record.Position) error
  Stop(context.Context) error
  Teardown(context.Context) error
}

type DestinationPlugin interface {
  Configure(context.Context, map[string]string) error
  Start(context.Context) error
  Write(context.Context, record.Record) error
  Ack(context.Context) (record.Position, error)
  Stop(context.Context) error
  Teardown(context.Context) error
}

type SpecifierPlugin interface {
  Specify() (Specification, error)
}
```

The methods roughly map to the methods defined in the gRPC interface described in the previous chapter. There are 2
things to point out

- These interfaces use internal structs in the Conduit codebase (e.g. `record.Position`, `record.Record`), so
  interacting with a plugin is seamless.
- The bidirectional stream method `Run` is broken down into two separate methods, one for receiving messages from the
  stream and one to send messages. Those methods can only be called after a call to `Start` since that is the method in
  which the stream actually gets opened.
  - In `SourcePlugin` we can read records from the stream by calling `Read`. This method will block until either an
    error occurs or a new record is produced by the plugin. Successfully processed records can be acknowledged by
    calling `Ack` with the corresponding record position.
  - In `DestinationPlugin` we can write records to the stream by calling `Write`. To receive acknowledgments we can
    call `Ack` which will block until either an error occurs or an acknowledgment is produced by the plugin.

### Plugin registries

![Plugin Architecture - Registries](https://user-images.githubusercontent.com/8320753/153414135-83f3c196-5d1e-4b03-8172-0427c0a01c03.svg)

We introduced a plugin dispenser interface with two implementations that allow us to interact with the plugin either
through go-plugin (i.e. plugin runs in a standalone process) or directly through Go (i.e. plugin acts as a library).

```go
type Dispenser interface {
  DispenseSpecifier() (SpecifierPlugin, error)
  DispenseSource() (SourcePlugin, error)
  DispenseDestination() (DestinationPlugin, error)
}
```

The dispenser is responsible for managing the plugin lifecycle, from spinning up the plugin (i.e. starting the process)
to tearing it down (i.e. killing the process), depending on if there is any dispensed plugin left. There are two
implementations of a dispenser:

- The built-in dispenser will dispense plugins that communicate with the plugin via direct Go calls. This means that
  Conduit can import a plugin as any other library, register it in the built-in plugin registry and use it without
  having a separate binary for the plugin. The implementation behind the built-in plugin is using the thin layer defined
  in the connector-plugin repository (see previous chapter), meaning that it is irrelevant which SDK the plugin uses (if
  any), as long as it implements the interfaces defined in that thin layer.
- The standalone dispenser will dispense plugins that communicate with the plugin via go-plugin and gRPC. It will start
  the plugin in a separate process as soon as a call to dispense a plugin comes in. When the method `Teardown` gets
  called the dispenser regards the plugin as done and the plugin process will be stopped. If then a new plugin tries to
  be dispensed the process will be started again. Note that the specifier plugin does not contain a `Teardown` method so
  a call to `Specify` will signal to the dispenser that the plugin process can be stopped.

The plugin registry is the last missing piece that contains information about the available plugins.

```protobuf
type registry interface {
  New(logger log.CtxLogger, name string) (Dispenser, error)
}
```

Behind this interface, there are again two registries that contain either built-in or standalone dispensers. To
differentiate which registry should be used we use a prefix in the name of the plugin - any plugin that starts
with `builtin:` will be taken from the built-in registry. Note that the naming of the prefix is not final and is subject
to change.

### Conduit plugin SDK

The Conduit plugin SDK is currently a package in Conduit but is written as a standalone package that does not depend on
any internal structs from Conduit. This will aid in extracting the SDK into a separate repository in the future.

The goal of the SDK package is to hide the complexity and verbosity of the plugin interface and provide utilities for
writing a plugin (e.g. leveled and structured logging, backoff retries).

```go
type Source interface {
  Configure(context.Context, map[string]string) error
  Open(context.Context, Position) error
  Read(context.Context) (Record, error)
  Ack(context.Context, Position) error
  Teardown(context.Context) error

  mustEmbedUnimplementedSource()
}
```

First, any struct that wants to implement `Source` needs to embed `UnimplementedSource`, this is the same pattern that
gRPC uses to ensure the struct is forward compatible in case we change the interface in the future. This is especially
important for built-in plugins, where plugins are imported as libraries - in that case, Conduit will use the latest SDK
version that any of the imported plugins depend on.

- `Configure` is the first function to be called in a plugin. It provides the plugin with the configuration that needs
  to be validated and stored. In case the configuration is not valid it should return an error.
- `Open` is called after `Configure` to signal the plugin it can prepare to start producing records at the requested
  position. If needed, the plugin should open connections in this function.
- `Read` returns a new record and is supposed to block until there is either a new record or the context gets cancelled.
  It can also return the error `ErrBackoffRetry` to signal to the SDK it should call `Read` again with a backoff retry.
  It's guaranteed that `Read` will be called with a cancelled context at least once, or that the context will get
  cancelled while `Read` is running. In that case, `Read` must stop retrieving new records from the source system and
  start returning records that may have already been buffered. If there are no buffered records left `Read` must return
  the context error to signal a graceful stop. After `Read` returns an error the function won't be called again (except
  if the error is `ErrBackoffRetry`, as mentioned above). `Read` can and will be called concurrently with `Ack`.
- `Ack` signals to the implementation that the record with the supplied position was successfully processed. This method
  might be called after the context of `Read` is already cancelled, since there might be outstanding acks that need to
  be delivered. When `Teardown` is called it is guaranteed there won't be any more calls to `Ack`.
  `Ack` can and will be called concurrently with `Read`.
- `Teardown` signals to the plugin that there will be no more calls to any other function. After Teardown returns, the
  plugin should be ready for a graceful shutdown.

```go
type Destination interface {
  Configure(context.Context, map[string]string) error
  Open(context.Context) error
  WriteAsync(context.Context, Record, AckFunc) error
  Write(context.Context, Record) error
  Flush(context.Context) error
  Teardown(context.Context) error

  mustEmbedUnimplementedDestination()
}
```

Any struct that wants to implement `Destination` needs to embed `UnimplementedDestination`. For reasoning see
the `UnimplementedSource` explanation above.

Notice that there are two methods for writing the record, `WriteAsync` and `Write`. The async method takes precedence
and allows plugins to send acknowledgments back at a later time. If `WriteAsync` is not implemented the SDK will
call `Write` instead.

- `Configure` is the first function to be called in a plugin. It provides the plugin with the configuration that needs
  to be validated and stored. In case the configuration is not valid it should return an error.
- `Open` is called after `Configure` to signal the plugin it can prepare to start writing records. If needed, the plugin
  should open connections in this function.
- `WriteAsync` receives a record and can cache it internally and write it at a later time. Once the record is
  successfully written to the destination the plugin must call the provided `AckFunc` function with a nil error. If the
  plugin failed to write the record to the destination it must call the supplied `AckFunc` with a non-nil error. If
  any `AckFunc` is left uncalled the connector will not be able to exit gracefully. The `WriteAsync` function should
  only return an error in case a critical error happened, it is expected that all future calls to `WriteAsync` will fail
  with the same error. If the plugin caches records before writing them to the destination it needs to store
  the `AckFunc` as well and call it once the record is written. `AckFunc` will panic if it's called more than once.
  If `WriteAsync` returns `ErrUnimplemented` the SDK will fall back and call `Write` instead.
- `Write` receives a record and is supposed to write the record to the destination right away. If the function returns
  nil the record is assumed to have reached the destination.
  `WriteAsync` takes precedence and will be tried first to write a record. If `WriteAsync` is not implemented the SDK
  will fall back and use `Write` instead.
- `Flush` signals the plugin it should flush any cached records and call all outstanding `AckFunc` functions. No more
  calls to `Write` will be issued after `Flush` is called.
- `Teardown` signals to the plugin that all records were written and there will be no more calls to any other function.
  After `Teardown` returns, the plugin should be ready for a graceful shutdown.

```go
func Serve(
  specFactory func () Specification,
  sourceFactory func() Source,
  destinationFactory func () Destination,
)
```

The plugin SDK provides a method `Serve` that should be called in the `main` function to start serving the plugin. It
expects functions that will be called when a `Specification`, `Source` or `Destination` is actually requested so that
those can be lazily instantiated. In case a plugin only implements either the `Source` or the `Destination` it can
supply `nil` to the `Serve` function and it will automatically fall back to the unimplemented version of that interface.
`Serve` will block until the plugin should exit, so it takes care of the whole lifecycle of a standalone plugin. In the
case of a built-in plugin, this function won’t be called, since Conduit will directly use the `Specification`, `Source`
and `Destination` structs. For this to be possible the plugin needs to define these structs in a package that is not
called `main` (we suggest using the [/cmd/connector](https://github.com/golang-standards/project-layout/tree/master/cmd)
pattern for the main package).

## Consequences

- This approach successfully decouples Conduit from the plugin SDK, both can grow independently
- It hides the transport (gRPC) behind a thin layer
- It allows us to ship Conduit in a single binary with built-in plugins
- It allows us to easily generate code for other programming languages and potentially provide SDKs
- It improves the performance because records are streamed through the same open connection
- It provides an SDK that hides the complexity of the plugin interface and allows the developer to focus on the things
  that provide value

## Related

- [#84](https://github.com/ConduitIO/conduit/pull/84)
- [#85](https://github.com/ConduitIO/conduit/pull/85)
- [#86](https://github.com/ConduitIO/conduit/pull/86)
- [#87](https://github.com/ConduitIO/conduit/pull/87)
- [#88](https://github.com/ConduitIO/conduit/pull/88)
