## Processors

A processor is a component that operates on a single record that flows through a pipeline. It can either change the record
(i.e. **transform** it) or **filter** it out based on some criteria. Since they are part of pipelines, making yourself 
familiar with [pipeline semantics](/docs/pipeline_semantics.md) is highly recommended.

![Pipeline](data/pipeline_example.svg)

Processors are **optional** components in a pipeline, i.e. a pipeline can be started without them. They are always attached 
to a single parent, which can be either a connector or a pipeline. With that, we can say that we have the following types 
of processors:
1. **Source processors**: these processors only receive messages originating at a specific source connector. Source 
   processors are created by specifying the corresponding source connector as the parent entity.
2. **Pipeline processors**: these processors receive all messages that flow through the pipeline, regardless of the
   source or destination. Pipeline processors are created by specifying the pipeline as the parent entity.
3. **Destination processors**: these processors receive only messages that are meant to be sent to a specific
   destination connector. Destination processors are created by specifying the corresponding destination connector as the
   parent entity.

Given that every processor can have one (and only one) parent, processors cannot be shared. In case the same processing 
needs to happen for different sources or destinations, you have two options:
1. If records from all sources (or all destinations) need to be processed in the same way, then you can obviously create
a pipeline processor
2. If records from some, but not all, sources (or destinations) need to be processed in the same way, then you need to 
create multiple processors (one for each source or destination) and configure them in the same way.

## Adding and configuring a processor

Processors are created through the `/processors` endpoint. Here's an example:

```json lines
POST /v1/processors
{
    // name of the processor in Conduit
    // note that this is NOT a user-defined name for this processor
    "name": "extractfieldpayload",
    "parent": 
    {
        // type of parent: TYPE_CONNECTOR or TYPE_PIPELINE
        "type": "TYPE_CONNECTOR",
        // parent ID (connector ID in this case)
        "id": "aed07589-44d8-4c68-968c-1f6c5197f13b" 
    },
    "config":
    {
        "settings":
        {
            // configuration map for this processor
            "field": "name" 
        }
    }
}
```
The request to create a processor is described in [api.swagger.json](/pkg/web/openapi/swagger-ui/api/v1/api.swagger.json).

## Supported processors

Conduit provides a number of built-in processors, such as filtering fields, replacing them, posting payloads to HTTP endpoints etc.
Conduit also provides the ability to write custom processors in JavaScript.

### Built-in processors

An up-to-date list of all built-in processors and detailed descriptions can be found [here](https://pkg.go.dev/github.com/conduitio/conduit/pkg/processor/procbuiltin).
An example is available in [extract-field-transform.sh](/examples/processors/extract-field-transform.sh). The script will
set up a pipeline with the built-in extract-field processors.

### JavaScript processors

JavaScript processors make it possible to write custom processors in JavaScript. The API name for JavaScript processors 
(used in the request to create a processor) is `js`. There's only one configuration parameter, `script`, which is the 
script itself. To find out what's possible with the JS processors, also refer to the documentation for [goja](https://github.com/dop251/goja), 
which is the JavaScript engine we use.

Here's an example of a request payload to create a JavaScript processor:

```json
{
  "name": "js",
  "parent": {
    "type": "TYPE_CONNECTOR",
    "id": "d1ae72ea-9d9c-4bb2-b993-fdb7a01825ab"
  },
  "config": {
    "settings": {
      "script": "function process(record) {\n    record.Metadata[\"foo-key\"] = \"foo-value\";\n    return record;\n}\n"
    }
  }
}
```
The above will create a JavaScript processor (`"name": "js"`), attached to a connector (for the parent, we have 
`"type": "TYPE_CONNECTOR"`). The script used is:
```javascript
function process(record) {
    record.Metadata["foo-key"] = "foo-value";
    return record;
}
```

The script needs to define a function called `process`, which accepts an `sdk.Record`, and returns:
* an `sdk.Record`, in case you want to transform the record,
* `null`, in case you want to drop the record from the pipeline.

The above example request transforms a record, by "enriching" its metadata (it adds a metadata key). Following is an 
example where we also filter records:
```javascript
function process(r) {
    // if the record metadata has a "keepme" key set
   // we will keep it.
   // otherwise we return null (i.e. we drop the record from the pipeline)
    if (r.Metadata["keepme"] != undefined) {
        return r
    }
    return null;
}
```

The script, of course, is not constrained to having only this function, i.e. you can have something like this:
```javascript
function doSomething(record) {
    // do something with the record
    return record
}

function process(record) {
    doSomething(record)
    return record
}
```

Conduit also provides a number of helper objects and methods which can be used in the JS code. Those are, currently:
1. `logger` - a `zerolog.Logger` which writes to the Conduit server logs. You can use it in the same way you would use 
it in Go code, i.e. you can write this for example: `logger.Info().Msgf("hello, %v!", "world")`
2. `Record()` - constructs a `record.Record`.
3. `RawData()` - constructs `record.RawData`.

Following is an example of a JavaScript processor, where we transform a record and utilize a number of tools mentioned 
above:
```javascript
// Parses the record payload as JSON
function parseAsJSON(record) {
    // we can, of course, use all of the JavaScript built-in functions
    // we use the record as if we would use it Go code, 
    // so record.Payload.Bytes() gives us the payload bytes
    return JSON.parse(String.fromCharCode.apply(String, record.Payload.Bytes()))
}

function process(record) {
    logger.Info().Msg("entering process");

    let json = parseAsJSON(record);
    json["greeting"] = "hello!";
    logger.Info().Msgf("json: %v", json);

    // we're creating a new RawData object, using a helper
    record.Payload = RawData();
    record.Payload.Raw = JSON.stringify(json);

    logger.Info().Msg("exiting process");
    return record;
}
```
