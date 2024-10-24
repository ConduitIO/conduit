# Better Processors

## Introduction

### Background

Conduit processors are currently pretty basic. We would like to make them more powerful
to give users more freedom when manipulating in-flight data.

Initially, processors were inspired by single message transforms (SMTs) provided by
Kafka Connect. We tried to mimic the behavior as well as processor names to bring them
closer to new Conduit users who had prior experience with Kafka Connect. A couple of
issues arose because of this. The main issue is that Conduit is working with a single
OpenCDC record that combines the key and payload, whereas Kafka Connect has separate
payloads for both. The OpenCDC record additionally provides fields like metadata, position,
operation, and two fields for the payload (before and after). Conduit also differentiates
between raw data and structured data. All of this meant we tried to jam a square peg into
a round hole and ended up with sub-par processors.

This document tries to address these issues holistically and create a better experience
around processors.

### Goals

- Keep existing processors to preserve backward compatibility.
  - Old processors should only be deprecated but continue to work.
- Add the ability to run a processor only on records that match a certain condition.
- Generate documentation for processors from code.
  - Ensure the generated documentation includes an example usage of each processor.
- Prepare a processor interface that won't have to change if/when we introduce merging
  and splitting of records and micro-batching.
- Create generic processors that can work on any field of the OpenCDC record (or a subset
  of fields, when applicable) instead of separate processors working on different fields.
- Restructure existing processors (group similar processors, split some processors) and
  choose naming that will make them easier to understand for users.
- Be able to list available processors in the HTTP/gRPC API.
- Allow users to plug in their own processor without recompiling Conduit.
- Expand the processor lifecycle to enable implementing more complex processors that
  require an initialization and shutdown.

### Questions

None yet.

## Implementation options

In this section, we present the implementation options for achieving the behavior around
processors like the lifecycle and addressing fields in a record. Note that the exhaustive
list of processors is in the next chapter.

### Processor Lifecycle

Processors are currently very simple entities with a very simple lifecycle. They receive
a configuration on creation and have only one method that is executed once for every
record that flows through the processor. The approach is simple and fulfilled our needs until now.

Given the goal to create pluggable processors and possibly stateful processors in the future,
we need to expand the interface and give processors more information about their lifecycle. We
specifically want to let the processor know when it's time to open any resources it may need and
when it should release those resources as the pipeline is shutting down. This will make it possible
to write more complex processors.

We already have interfaces that provide similar functionality for connectors, namely
[Source](https://github.com/ConduitIO/conduit-connector-sdk/blob/4d9c6810b040e7a13f06f2823b62b6af9f10edf4/source.go#L43)
and [Destination](https://github.com/ConduitIO/conduit-connector-sdk/blob/4d9c6810b040e7a13f06f2823b62b6af9f10edf4/destination.go#L37)
in the connector SDK. Since these interfaces have served us well, we propose to use a similar
interface to control the processor lifecycle:

```go
// Processor receives records, manipulates them and returns back the processed
// records.
type Processor interface {
	// Specification contains the metadata of this processor like name, version,
	// description and a list of parameters expected in the configuration.
	Specification() Specification

	// Configure is the first function to be called in a processor. It provides the
	// processor with the configuration that needs to be validated and stored.
	// In case the configuration is not valid it should return an error.
	// Configure should not open connections or any other resources. It should solely
	// focus on parsing and validating the configuration itself.
	Configure(context.Context, map[string]string) error

	// Open is called after Configure to signal the processor it can prepare to
	// start writing records. If needed, the processor should open connections and
	// start background jobs in this function.
	Open(context.Context) error

	// Process takes a number of records and processes them right away.
	// It should return a slice of ProcessedRecord that matches the length of
	// the input slice. If an error occurred while processing a specific record
	// it should be reflected in the ProcessedRecord with the same index as the
	// input record that caused the error.
	// Process should be idempotent, as it may be called multiple times with the
	// same records (e.g. after a restart when records were not flushed).
	Process(context.Context, []Record) ([]ProcessedRecord)

	// Teardown signals to the processor that the pipeline is shutting down and
	// there will be no more calls to any other function. After Teardown returns,
	// the processor will be discarded.
	Teardown(context.Context) error
}
```

Notice that the processor returns its own specification in the first method. This
is required so that Conduit can build a registry of all available processors
and give the user more information about them (see
[Listing processor plugins](#listing-processor-plugins)).

A few notes about the `Process` method:

- Even though we are not adding support for processing multiple records at the
  same time, we propose to design the interface in a way that will let us add
  that functionality in the future without breaking changes. This means that the
  function will for now be called only with a single record.
- Notice that the return value is a slice of type `ProcessedRecord`. The purpose
  behind this is to be able to figure out what happened to each input record. This
  way we can associate each input record to an output record and know what happened
  to it (was it filtered out, split, merged, or only transformed). This distinction
  is important because of acknowledgments (e.g. if a record is split into multiple
  records, Conduit will acknowledge the original record only once all split records
  are acknowledged).
- If an error happens in `Process` it should be stored in the `ProcessedRecord` that
  was being processed when the error occurred. In that case the remaining records
  can be filled with a specific `ProcessedRecord` that marks them as not processed.
  In that case Conduit will determine if it's required to process those records and
  pass them to the processor in the next call to `Process`.

### Listing processor plugins

We have to differentiate listing actual processors that are attached to actual pipelines
and listing the specifications of all available processors. We can draw a parallel to
listing connectors vs. listing connector plugins. We already have a mechanism to list
actual processors using
[`ProcessorService.ListProcessors`](https://github.com/ConduitIO/conduit/blob/df048d93e6240c2c3ec5e52af430c05bdfc7a1a3/proto/api/v1/api.proto#L713),
we need to add a way to list available processors.

Since we are adding pluggable processors we propose to use the same terminology as with
connectors and introduce **processor plugins**. This would also give us the ability to
treat them in a similar way and expose them to the user through the plugin service. We
propose to change the plugin service so that it exposes connector plugins and processor
plugins on separate endpoints. For backwards compatibility we would still expose connector
plugins on the same endpoint as before (this would be removed before the v1.0 release).

```protobuf
service PluginService {
  // ListPlugins is a DEPRECATED. It returns a list of all connector plugins.
  rpc ListPlugins(ListPluginsRequest) returns (ListPluginsResponse) {
    option (google.api.http) = {
      get: "/v1/plugins"
      response_body: "plugins"
    };
  };

  // ListConnectorPlugins returns a list of all connector plugins.
  rpc ListConnectorPlugins(ListConnectorPluginsRequest) returns (ListConnectorPluginsResponse) {
    option (google.api.http) = {
      get: "/v1/plugins/connectors"
      response_body: "connectors"
    };
  };

  // ListProcessorPlugins returns a list of all processor plugins.
  rpc ListProcessorPlugins(ListProcessorPluginsRequest) returns (ListProcessorPluginsResponse) {
    option (google.api.http) = {
      get: "/v1/plugins/processors"
      response_body: "processors"
    };
  };
}
```

Note that builtin processor plugins would be listed in this endpoint alongside
standalone plugins.

### Pluggable processors and SDK

We want to give Conduit users the ability to provide their own processor in form of a
plugin. We propose a similar approach to pluggable processors as we already have with
pluggable connectors. This means that processors would be loaded at startup and added
to the processor plugin registry. Processors would be addressable using a similar naming
scheme as connectors (more on
[referencing connectors](https://conduit.io/docs/using/connectors/referencing)). Built-in
processors would be created in a similar manner as built-in connectors, in that they
would implement the same interface and could essentially be extracted into standalone
processors (more info on
[standalone vs built-in connectors](https://conduit.io/docs/connectors/behavior#standalone-vs-built-in-connectors)).

The only difference between pluggable processors and connectors is the proposed plugin
mechanism. For pluggable processors we are leaning towards using
[WebAssembly](https://webassembly.org). We are not decided yet which runtime to use, but
it will most likely be either [wazero](https://github.com/tetratelabs/wazero) or
[wasmtime-go](https://github.com/bytecodealliance/wasmtime-go). The benefit of wazero is
the fact that it is written in pure Go and does not require CGO. On the other hand,
wasmtime-go is using [wasmtime](https://github.com/bytecodealliance/wasmtime) behind the
scenes, which is a popular runtime written in Rust and might provide better performance.

The final decision on the chosen runtime will be done as part of the implementation.

We will also need to provide an SDK for users who want to implement their own processor.
Our proposal is to again mimic the structure we use for connectors and create a separate
module (e.g. `github.com/conduitio/conduit-processor-sdk`) that exposes a simple SDK for
implementing a processor plugin. This SDK should provide utilities for logging and
calling exposed host functions. An example of such an SDK is the
[Redpanda transform SDK](https://github.com/redpanda-data/redpanda/tree/dev/src/transform-sdk/go).

### Conditional execution of processors

Currently the only way to control what record gets passed to the processor is by
attaching the processor either to a source or a destination connector. It would
be useful if the user could additionally define a condition that must be fulfilled for
the record to get passed to a processor.

We already have a similar functionality in the `filterfieldkey` processor which lets you
filter a record if some condition is met. The idea is to extract this functionality and
create a generalized way to create conditions on processors. If the condition evaluates
to `false`, the processor is skipped and the record is passed along to the next node
without being processed.

This way we can simplify the `filter` processor and, more importantly, give the user
the control of defining when a processor should be executed.

We propose adding a field `condition` in the processor configuration that allows you to
write a Go template that returns a boolean. If the returned value evaluates to `true`,
the processor will be executed, otherwise it will be skipped.
If the condition field is empty it is the same as setting the condition to `true`.
In other words, by default all records are passed to the processor.

```yaml
processors:
  id:   example
  type: custom.javascript
  # Only execute processor if the metadata field "opencdc.version" contains value "v1"
  condition: {{ eq .Metadata.opencdc.version "v1" }}
  settings:
    script: >
      function process(record) {
        logger.Info().Msg("executing JavaScript processor on OpenCDC v1 record");
        return record;
      }
```

### Addressing record fields

Anytime a processor contains a setting that allows you to pick a field or write a
condition we recommend using the same syntax as the Go stdlib package
[text/template](https://pkg.go.dev/text/template). This way we are leaning on syntax
that is thoroughly documented and has a lot of resources online outside of the
Conduit ecosystem. Additionally, we already use the same approach in specifying the
record [output format](https://conduit.io/docs/connectors/output-formats).

Here are a few examples of addressing record fields:

- `.` - references the whole record
- `.Payload.After` - references the field that normally carries the payload
- `{{ eq .Key.foo "123" }}` - evaluates to `true` if the key contains the value
  "123" in field `foo`

Note that referencing a field does not require the expression to be nested inside of
brackets (`{{...}}`), as we don't want the expression to be evaluated. We are simply
referencing a field with the same syntax we would in a Go template. On the other hand,
when we **do** want the expression to be evaluated, we need to put the expression in
brackets. This allows us to differentiate between static values and expressions.

Go templates evaluate to a string, therefore we need to think about how we
plan to handle cases when we don't expect a string as an output. For instance,
conditions need to evaluate to a boolean value. We propose to simply use the package
`strconv`, specifically the function
[`strconv.ParseBool`](https://pkg.go.dev/strconv#ParseBool) to convert the result to
a boolean value. This means that values 1, t, T, TRUE, true, True are converted to
`true`, while 0, f, F, FALSE, false and False are converted to `false`. Any value
that can not be converted to `true` or `false` will return an error which means the
record gets passed to the DLQ handler.

In our evaluations of template engines `text/template` seems to be performant enough
for our use case. Evaluating a simple template is currently faster than the measured
maximum throughput of Conduit itself, to be precise, it is 10x faster, so we don't
expect it to be a bottleneck.

### Documenting processors

Having the best processors doesn't help our users if they don't know how to use them.
Therefore, it is important to define how we are going to document them and maintain the
documentation up to date.

The processor documentation will be written by developers implementing the processors,
so it would make sense to keep the documentation close to the code. Our recommendation
is to use Go comments for this purpose. We want to take a similar approach like
[paramgen](https://github.com/ConduitIO/conduit-connector-sdk/tree/main/cmd/paramgen)
in the connector SDK. The developer should be able to write the documentation once in
a Go idiomatic way and use a tool to extract it into a format that can be used on the
[documentation website](https://conduit.io/docs).

We can use the same tool to extract the documentation into the connector specification
(see [processor lifecycle](#processor-lifecycle)). This way the same documentation can
be retrieved programmatically and returned in API responses, which in turn makes it
possible to display it to users in the UI without the need to leave the page.
Additionally, it allows us to ship the documentation for custom pluggable processors
that might not be documented on the official Conduit website.

Ideally, each processor would also provide an example of its usage and a comparison
of the record before and after the execution. The tool for extracting documentation
should be able to extract the example separately so that it can be displayed separate
from the processor description.

## Processors

This section is addressing a larger number of processors, where each processor could
potentially have its own design document. Since these processors need to make sense
together and cover the goals collectively, we grouped them into this single design
document and focus only on the recommended implementation of the bigger picture. In
other words: we do not present options for different _sets_ of processors. However, on
the level of a single processor we have sometimes identified different options for
certain implementation details, and that is where we actually present the options and
recommendation separately.

Here is an overview of the recommended set of processors:

- `field.set` - set a field in the record
- `field.subset.exclude` - remove fields from the record if they are on the list
- `field.subset.include` - remove fields from the record if they _are not_ on the list
- `field.rename` - rename fields
- `field.flatten`* - recursively flatten a structured field
- `field.convert` - convert one type into another in a structured payload
- `filter` - ack record and remove it from the pipeline
- `decode.avro` - decode Avro data into structured data
- `decode.json` - decode a JSON string into structured data
- `encode.avro` - encode structured data into Avro data
- `encode.json` - encode structured data into a JSON string
- `unwrap.opencdc`* - unwrap an OpenCDC payload into the current record
- `unwrap.kafkaconnect` - unwrap a Kafka Connect payload into the current record
- `unwrap.debezium` - unwrap a Debezium payload into the current record
- `webhook.http` - send a HTTP request for each record
- `webhook.grpc`* - send a gRPC request for each record
- `custom.javascript` - execute JavaScript code for each record

\*Processors marked with a star are planned in the future and not part of the
"Better Processors" work.

### `field.set`

This is a powerful processor that lets you set any field in an OpenCDC record.
It replaces `extractfield`, `hoistfield`, `insertfield`, `maskfield` and
`valuetokey`.

To make this processors work, all fields in a record need to have a .String() method
that represents the field as JSON (i.e. raw data is represented as a base64 encoded
string, position as well, metadata is formatted as a JSON map etc.). The result of
evaluating the `value` template will therefore be a JSON string that we can convert
back to a Go type. Based on which field we are setting we would automatically detect
if the JSON can be converted to that type, otherwise we would leave it as a string.

To illustrate, if we are setting the field `.Payload.After` which is a field that takes
either structured data or raw data (bytes), we could convert the resulting string into
structured data if it is a JSON map, otherwise we would use the string as raw data.

Similarly, if the target field is `.Metadata.foo` we know that the result has to be a
string, therefore we don't have to do any conversion.

#### Configuration

- `field` (required) - is the target field, as it would be addressed in a Go template.
- `value` (required) - is a Go template expression which will be evaluated and stored in `field`.

#### Example

- copy field "foo" from the key to the payload

In this case `.Payloa.After` needs to contain structured data, otherwise the
processor returns an error.

```yaml
processors:
  id:   example
  type: set.field
  settings:
    field: ".Payload.After.foo"
    value: "{{ .Key.foo }}"
```

- set field "bar" in the key to the static value "hidden"

```yaml
processors:
  id:   example
  type: set.field
  settings:
    field: ".Key.bar"
    value: "hidden"
```

- hoist the payload into field "hoisted"

This example is a bit more complicated. The result of evaluating the value is a
JSON object with a single field named "hoisted" and the value set to the contents
of `.Payload.After`. The result is a valid JSON object which will be converted back
to structured data before stored in field `.Payload.After`.

```yaml
processors:
  id:   example
  type: set.field
  settings:
    field: ".Payload.After"
    value: '{"hoisted":{{ .Payload.After }}}'
```

### `field.subset.exclude`

Remove a subset of fields from the record, all other fields are left untouched.
If a field is excluded that contains nested data, the whole tree will be removed.
It is not allowed to exclude `.Position` or `.Operation`.

#### Configuration

- `fields` (required) - comma separated fields that should be excluded from the record.

#### Example

Exclude all metadata fields, field "foo" in `.Payload.Before` and field "bar" in `.Key`.

```yaml
processors:
  id:   example
  type: field.subset.exclude
  settings:
    fields: ".Metadata,.Payload.Before.foo,.Key.bar"
```

### `field.subset.include`

Include only a subset of fields in the record.
If a field is included that contains nested data, the whole tree will be included.
This processor is not applicable to `.Position` and `.Operation`. The remaining
record fields are included by default (`.Key`, `.Metadata`, `.Payload.Before`,
`.Payload.After`) unless the configuration contains a field that is nested in one
of these record fields. For example, if the user configures to include field
`.Metadata.foo` then only field `foo` will be included in `.Metadata` while all other
metadata fields will be removed, the remaining record fields won't be changed
(`.Key`, `.Payload.Before`, `.Payload.After`).

#### Configuration

- `fields` (required) - comma separated fields that should be included in the record.

#### Example

Include only fields `foo` and `bar` in `.Key`, remove all other fields from `.Key`.

```yaml
processors:
  id:   example
  type: field.subset.include
  settings:
    fields: ".Key.foo,.Key.bar"
```

The following configuration is equivalent to the previous one:

```yaml
processors:
  id:   example
  type: field.subset.include
  settings:
    fields: ".Key.foo,.Key.bar,.Payload.Before,.Payload.After,.Metadata"
```

### `field.rename`

Rename a group of fields. It is not allowed to rename top-level fields (`.Operation`,
`.Position`, `.Key`, `.Metadata`, `.Payload.Before`, `.Payload.After`), only fields
that are nested.

#### Configuration

- `mapping` (required) - comma separated key:value pairs, where the key is the target
  field reference and value is the name of the new field (only name, not the full path).

#### Example

```yaml
processors:
  id:   example
  type: field.rename
  settings:
    mapping: ".Key.foo:bar,.Payload.Before.id:key,.Metadata.topic:subject"
```

### `field.flatten`

Flatten takes a field that is structured or a map and flattens all nested values into
top level values. The keys leading to a nested value are concatenated with a separator,
by default the separator is a dot (`.`).

This processor is only applicable to `.Key`, `.Payload.Before` and `.Payload.After`, as
it only runs on structured data.

#### Configuration

- `field` (required) - is the target field, as it would be addressed in a Go template.
- `separator` (default=`.`) - is the separator used when concatenating nested field keys.

#### Example

This example collapses all nested fields in `.Payload.After` and puts the values in top
level fields. Top level fields are constructed using the `.` as a separator between
nested field keys.

```yaml
processors:
  id:   example
  type: field.flatten
  settings:
    field: ".Payload.After"
```

We can also collapse only part of the structured payload and specify a custom separator:

```yaml
processors:
  id:   example
  type: field.flatten
  settings:
    field: ".Payload.After.foo"
    separator: "-"
```

### `field.convert`

Convert takes the field of one type and converts it into another type (e.g. string to
integer). The applicable types are `string`, `int`, `float` and `bool`. Converting can
be done between any combination of types. Note that booleans will be converted to
numeric values 1 (true) and 0 (false).

This processor is only applicable to `.Key`, `.Payload.Before` and `.Payload.After`, as
it only runs on fields nested in structured data.

#### Configuration

- `field` (required) - is the target field, as it would be addressed in a Go template.
- `type` (required) - is the target field type after conversion.

#### Example

This example converts the field "id" in `.Key` to an integer.

```yaml
processors:
  id:   example
  type: field.convert
  settings:
    field: ".Key.id"
    type: 'int'
```

### `filter`

Filter acknowledges all records that get passed to the filter. There is no further
configuration of the processor needed, as the passing of records to the filter
should be controlled using a condition on the processor itself.

#### Configuration

No configuration, it is expected to be used in conjunction with `condition`.

#### Example

Filter out records that contain value "bar" in the metadata field "foo".

```yaml
processors:
  id:   example
  type: filter
  condition: '{{ eq .Metadata.foo "bar" }}'
```

### `decode.avro`

This processor is the same as the old processor
[`decodewithschema`](https://conduit.io/docs/using/processors/builtin/#decodewithschemakey),
except that it focuses solely on decoding Avro values. If we add support for more
formats in the future it will be in the shape of new processors. This way the
processors will be easier to discover and we can tailor the processor configuration
to the specific format and provide custom options when required. Behind the scenes
we can still reuse the same code for most of the processor.

#### Configuration

Taken from [existing docs](https://conduit.io/docs/using/processors/builtin/#configuration).

- `url` (required) - URL of the schema registry (e.g. `http://localhost:8085`).
- `auth.basic.username` (optional) - Configures the username to use with basic
  authentication. This option is required if auth.basic.password contains a value.
  If both `auth.basic.username` and `auth.basic.password` are empty basic authentication
  is disabled.
- `auth.basic.password` (optional) - Configures the password to use with basic
  authentication. This option is required if auth.basic.username contains a value.
  If both `auth.basic.username` and `auth.basic.password` are empty basic authentication
  is disabled.
- `tls.ca.cert` (optional) - Path to a file containing PEM encoded CA certificates.
  If this option is empty, Conduit falls back to using the host's root CA set.
- `tls.client.cert` (optional) - Path to a file containing a PEM encoded certificate.
  This option is required if `tls.client.key` contains a value. If both `tls.client.cert`
  and `tls.client.key` are empty TLS is disabled.
- `tls.client.key` (optional) - Path to a file containing a PEM encoded private key.
  This option is required if `tls.client.cert` contains a value. If both `tls.client.cert`
  and `tls.client.key` are empty TLS is disabled.

#### Example

Taken from [existing docs](https://conduit.io/docs/using/processors/builtin/#example).

```yaml
processors:
  id:   example
  type: decode.avro
  settings:
    url:                 "http://localhost:8085"
    auth.basic.username: "user"
    auth.basic.password: "pass"
    tls.ca.cert:         "/path/to/ca/cert"
    tls.client.cert:     "/path/to/client/cert"
    tls.client.key:      "/path/to/client/key"
```

### `decode.json`

This processor replaces the existing
[`parsejson`](https://conduit.io/docs/using/processors/builtin/#parsejsonkey) processor
and additionally gives the user the ability to specify which field should be decoded.
It takes raw data (string) from the target field, parses it as JSON and stores the
decoded structured data in the target field.

This processor is only applicable to `.Key`, `.Payload.Before` and `.Payload.After`, as
it only runs on structured data.

#### Configuration

- `field` (required) - is the target field, as it would be addressed in a Go template.

#### Example

```yaml
processors:
  id:   example
  type: decode.json
  settings:
    field: ".Payload.After"
```

### `encode.avro`

This processor is the same as the old processor
[`encodewithschema`](https://conduit.io/docs/using/processors/builtin/#encodewithschemakey),
except that it focuses solely on encoding Avro values. If we add support for more
formats in the future it will be in the shape of new processors. This way the
processors will be easier to discover and we can tailor the processor configuration
to the specific format and provide custom options when required. Behind the scenes
we can still reuse the same code for most of the processor.

Differences compared to the old processor:

- Removed configuration field `schema.autoRegister.format`, as the format is Avro.

#### Configuration

Taken from [existing docs](https://conduit.io/docs/using/processors/builtin/#configuration-1).

- `url` (required) - URL of the schema registry (e.g. `http://localhost:8085`).
- `schema.strategy` (required, Enum: `preRegistered`,`autoRegister`) - Specifies which
  strategy to use to determine the schema for the record. Available strategies:
  - `preRegistered` (recommended) - Download an existing schema from the schema registry.
    This strategy is further configured with options starting
    with `schema.preRegistered.*`.
  - `autoRegister` (for development purposes) - Infer the schema from the record and
    register it in the schema registry. This strategy is further configured with options
    starting with `schema.autoRegister.*`.
- `schema.preRegistered.subject` (required if `schema.strategy` = `preRegistered`) -
  Specifies the subject of the schema in the schema registry used to encode the record.
- `schema.preRegistered.version` (required if `schema.strategy` = `preRegistered`) -
  Specifies the version of the schema in the schema registry used to encode the record.
- `schema.autoRegister.subject` (required if `schema.strategy` = `autoRegister`) -
  Specifies the subject name under which the inferred schema will be registered in
  the schema registry.
- `auth.basic.username` (optional) - Configures the username to use with basic
  authentication. This option is required if auth.basic.password contains a value.
  If both `auth.basic.username` and `auth.basic.password` are empty basic authentication
  is disabled.
- `auth.basic.password` (optional) - Configures the password to use with basic
  authentication. This option is required if auth.basic.username contains a value.
  If both `auth.basic.username` and `auth.basic.password` are empty basic authentication
  is disabled.
- `tls.ca.cert` (optional) - Path to a file containing PEM encoded CA certificates.
  If this option is empty, Conduit falls back to using the host's root CA set.
- `tls.client.cert` (optional) - Path to a file containing a PEM encoded certificate.
  This option is required if `tls.client.key` contains a value. If both `tls.client.cert`
  and `tls.client.key` are empty TLS is disabled.
- `tls.client.key` (optional) - Path to a file containing a PEM encoded private key.
  This option is required if `tls.client.cert` contains a value. If both `tls.client.cert`
  and `tls.client.key` are empty TLS is disabled.

#### Example

Taken from [existing docs](https://conduit.io/docs/using/processors/builtin/#example-1).

```yaml
processors:
  id:   example
  type: encode.avro
  settings:
    url:                          "http://localhost:8085"
    schema.strategy:              "preRegistered"
    schema.preRegistered.subject: "foo"
    schema.preRegistered.version: 2
    auth.basic.username:          "user"
    auth.basic.password:          "pass"
    tls.ca.cert:                  "/path/to/ca/cert"
    tls.client.cert:              "/path/to/client/cert"
    tls.client.key:               "/path/to/client/key"
```

### `encode.json`

This processor takes structured data from the target field, encodes it as a JSON string
and stores it in the target field.

This processor is only applicable to `.Key`, `.Payload.Before` and `.Payload.After`, as
it only runs on structured data.

#### Configuration

- `field` (required) - is the target field, as it would be addressed in a Go template.

#### Example

```yaml
processors:
  id:   example
  type: encode.json
  settings:
    field: ".Payload.After"
```

### `unwrap.opencdc`

We are skipping the design of this processor, as it will not be part of the
"Better Processors" milestone. We are still mentioning it to paint the full picture.

### `unwrap.kafkaconnect`

This processor together with `unwrap.debezium` replaces the existing
[`unwrap`](https://conduit.io/docs/using/processors/builtin/#unwrap) processor, but works
the same way under the hood. It additionally gives the user the ability to specify
which field is the source of the kafkaconnect record.

This processor is only applicable to `.Key`, `.Payload.Before` and `.Payload.After`, as
it only runs on structured data. Note that you might need to use `decode.json` before
running this processor.

#### Configuration

- `field` (required) - is the target field, as it would be addressed in a Go template.

#### Example

```yaml
processors:
  id:   example
  type: unwrap.kafkaconnect
  settings:
    field: ".Payload.After"
```

### `unwrap.debezium`

This processor together with `unwrap.kafkaconnect` replaces the existing
[`unwrap`](https://conduit.io/docs/using/processors/builtin/#unwrap) processor, but works
the same way under the hood. It additionally gives the user the ability to specify
which field is the source of the debezium record.

This processor is only applicable to `.Key`, `.Payload.Before` and `.Payload.After`, as
it only runs on structured data. Note that you might need to use `decode.json` before
running this processor.

#### Configuration

- `field` (required) - is the target field, as it would be addressed in a Go template.

#### Example

```yaml
processors:
  id:   example
  type: unwrap.debezium
  settings:
    field: ".Payload.After"
```

### `webhook.http`

This processor replaces the existing
[`httprequest`](https://conduit.io/docs/using/processors/builtin/#httprequest) processor,
but works the same way under the hood. It additionally gives the user the ability
to specify what is sent as the body and where the response body and status are
stored (configuration options `request.body`, `response.body` and `response.status`).

#### Configuration

Taken from [existing docs](https://conduit.io/docs/using/processors/builtin/#configuration-5).

- `request.url` (required) - URL used in the HTTP request.
- `request.method` (optional, default=`POST`) - HTTP request method to be used.
- `request.body` (optional, default=`.`) - The source field that is encoded as JSON
  and sent in the body of the request. The path to the field is formatted as it would be
  addressed in a Go template.
- `response.body` (optional, default=`.Payload.After`) - The target field in which the
  response body will be stored (raw string). It replaces the field if it already exists.
  To process it further use other processors.
- `response.status` (optional) - If set, the response HTTP status code will be stored
  in the target field.
- `backoffRetry.count` (optional, default=`0`) - Maximum number of retries when backing off
  following an error. Defaults to 0.
- `backoffRetry.factor` (optional, default=`2`) - The multiplying factor for each
  increment step.
- `backoffRetry.min` (optional, default=`100ms`) - Minimum waiting time before retrying.
- `backoffRetry.max` (optional, default=`5s`) - Maximum waiting time before retrying.

#### Example

```yaml
processors:
  id:   example
  type: webhook.http
  settings:
    request.url:     "https://localhost:5050/my-endpoint"
    request.method:  "GET"
    request.body:    "."
    response.body:   ".Payload.After"   # replace field with response
    response.status: ".Metadata.status" # store HTTP code in metadata
```

### `webhook.grpc`

We are skipping the design of this processor, as it will not be part of the
"Better Processors" milestone. We are still mentioning it to paint the full picture.

### `custom.javascript`

This processor replaces the existing `js` processor, but works the same way under the
hood. The only addition is that we should let the user specify a path to a file
containing the processor instead of forcing them to write it inline.

#### Configuration

- `script` (required) - is either the JavaScript code itself that should be executed
  for each record, or a path to a file containing the code.

#### Example

This creates a processor using the script provided in the configuration directly.

```yaml
processors:
  id:   example
  type: custom.javascript
  settings:
    script: >
      function process(record) {
        logger.Info().Msg("hello world");
        return record;
      }
```

The script can also be stored in a file

```yaml
processors:
  id:   example
  type: custom.javascript
  settings:
    script: ./processors/my-custom-processor.js
```
