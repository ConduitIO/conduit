## Transforms

A processor is a component that operates on a single record that flows through a pipeline. It can either change the record
(i.e. **transform** it) or **filter** it out based on some criteria. Since they are part of pipelines, making yourself 
familiar with [pipeline semantics](/docs/pipeline_semantics.md) is highly recommended.

![Pipeline](data/pipeline_example.svg)

Processors are **optional** components in a pipeline, i.e. a pipeline can be started without them. They can be attached 
either to a connector or to a pipeline. In other words, we have the following types of processors:
1. **Source processors**: these processors only receive messages originating at a specific source connector. Source 
processors are created by specifying the corresponding source connector as the parent entity.
2. **Pipeline processors**: these processors receive all messages that flow through the pipeline, regardless of the
   source or destination. Pipeline processors are created by specifying the pipeline as the parent entity.
3. **Destination processors**: these processors receive only messages that are meant to be sent to a specific
   destination connector. Destination processors are created by specifying the corresponding destination connector as the
   parent entity.



## Adding and configuring a processor

Processors are created through the `/processors` endpoint. Here's an example:

```json lines
POST /v1/processors
{
    // name of the transform
    "name": "extractfieldpayload",
    // type of processor: TYPE_TRANSFORM or TYPE_FILTER 
    "type": "TYPE_TRANSFORM",
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

## Supported transforms

Conduit provides a number of built-in transforms, such as filtering fields, replacing them, posting payloads to HTTP endpoints etc.
Conduit also provides the ability to write custom transforms in JavaScript.

### Built-in transforms

An up-to-date list of all built-in transforms and detailed descriptions can be found [here](https://pkg.go.dev/github.com/conduitio/conduit/pkg/processor/transform/txfbuiltin).
An example is available in [extract-field-transform.sh](/examples/transforms/extract-field-transform.sh). The script will
set up a pipeline with the built-in extract-field transform.

### JavaScript transforms

JavaScript transforms make it possible to write custom transforms. The API name for JavaScript transforms (used in the
request to create a transform) is `js`. There's only one configuration parameter, `script`, which is the script itself.

The script needs to implement a function called `transform`, which accepts an `sdk.Record`, and returns an `sdk.Record`:
```javascript
function transform(record) {
    // do something with the record
    return record
}
```

The script, of course, is not constrained to having only this function, i.e. you can have something like this:
```javascript
function doSomething(record) {
    // do something with the record
    return record
}

function transform(record) {
    doSomething(record)
    return record
}
```

Conduit also provides a number of helper objects and methods which can be used in the JS code. Those are, currently:
1. `logger` - a `zerolog.Logger` which writes to the Conduit server logs
2. `Record()` - constructs a `record.Record`.
3. `RawData()` - constructs `record.RawData`.

All the helpers are defined in [helpers.go](/pkg/processor/transform/txfjs/helpers.go). Following is an example of a JavaScript
transform:
```javascript
// Parses the record payload as JSON
function parseAsJSON(record) {
    return JSON.parse(String.fromCharCode.apply(String, record.Payload.Bytes()))
}

function transform(record) {
    logger.Info().Msg("entering transform");

    let json = parseAsJSON(record);
    json["greeting"] = "hello!";
    logger.Info().Msgf("json: %v", json);

    record.Payload = RawData();
    record.Payload.Raw = JSON.stringify(json);

    logger.Info().Msg("exiting transform");
    return record;
}
```
